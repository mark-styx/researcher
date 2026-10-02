package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"
	"github.com/marklubin/researchguy/internal/store"
)

// SyncStats is what Sync and Rebuild did.
type SyncStats struct {
	// Interrupted are runs Reconcile marked interrupted first.
	Interrupted []string          `json:"interrupted,omitempty"`
	Ingested    []RunStats        `json:"ingested"`
	UpToDate    int               `json:"up_to_date"`
	Failed      map[string]string `json:"failed,omitempty"` // run id: error
}

// Sync marks dead runs interrupted, then ingests every run that is new or
// has changed since it was last ingested. One run failing doesn't stop the
// rest; its error is in Failed.
func (ix *Index) Sync(ctx context.Context, st *store.Store) (SyncStats, error) {
	stats := SyncStats{Ingested: []RunStats{}}
	marked, err := st.Reconcile()
	stats.Interrupted = marked
	if err != nil {
		return stats, fmt.Errorf("reconciling runs: %w", err)
	}
	ids, total, err := ix.Pending(ctx, st)
	if err != nil {
		return stats, err
	}
	stats.UpToDate = total - len(ids)
	for _, id := range ids {
		rs, err := ix.IngestRun(ctx, st.RunDir(id), false)
		if err != nil {
			if stats.Failed == nil {
				stats.Failed = map[string]string{}
			}
			stats.Failed[id] = err.Error()
			continue
		}
		if rs.Skipped {
			stats.UpToDate++
			continue
		}
		stats.Ingested = append(stats.Ingested, rs)
	}
	return stats, nil
}

// Rebuild empties the index and ingests every run in the store, in one
// transaction: if it fails, the old index is still there.
func (ix *Index) Rebuild(ctx context.Context, st *store.Store) (SyncStats, error) {
	stats := SyncStats{Ingested: []RunStats{}}
	marked, err := st.Reconcile()
	stats.Interrupted = marked
	if err != nil {
		return stats, fmt.Errorf("reconciling runs: %w", err)
	}
	ids, err := st.RunIDs()
	if err != nil {
		return stats, err
	}
	err = pgx.BeginFunc(ctx, ix.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `TRUNCATE citations, passages, fetches, documents, sightings, captures, sources, runs`); err != nil {
			return fmt.Errorf("emptying index: %w", err)
		}
		for _, id := range ids {
			snap, err := readSnapshot(st.RunDir(id))
			if err != nil {
				// A run dir that can't be read is reported, not fatal.
				if stats.Failed == nil {
					stats.Failed = map[string]string{}
				}
				stats.Failed[id] = err.Error()
				continue
			}
			rs, err := ix.ingestSnapshot(ctx, tx, snap, true)
			if err != nil {
				return err
			}
			stats.Ingested = append(stats.Ingested, rs)
		}
		return nil
	})
	if err != nil {
		return SyncStats{Interrupted: marked, Ingested: []RunStats{}}, err
	}
	return stats, nil
}

// Pending lists the runs in st that the index doesn't have, or has as they
// were before their record, capture log, fetch log or citations last
// changed, and how many runs with a record the store holds. Run dirs without a run.json are skipped;
// store.Check reports them.
func (ix *Index) Pending(ctx context.Context, st *store.Store) ([]string, int, error) {
	type ingested struct {
		sha                          string
		captures, fetches, citations int64
	}
	indexed := map[string]ingested{}
	rows, err := ix.pool.Query(ctx, `SELECT id, record_sha256, capture_bytes, fetch_bytes, citation_bytes FROM runs`)
	if err != nil {
		return nil, 0, fmt.Errorf("listing indexed runs: %w", err)
	}
	for rows.Next() {
		var id string
		var got ingested
		if err := rows.Scan(&id, &got.sha, &got.captures, &got.fetches, &got.citations); err != nil {
			rows.Close()
			return nil, 0, err
		}
		indexed[id] = got
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	ids, err := st.RunIDs()
	if err != nil {
		return nil, 0, err
	}
	var pending []string
	total := 0
	for _, id := range ids {
		dir := st.RunDir(id)
		data, err := os.ReadFile(filepath.Join(dir, "run.json"))
		if err != nil {
			continue
		}
		total++
		sum := sha256.Sum256(data)
		var size int64
		if info, err := os.Stat(filepath.Join(dir, "captures.jsonl")); err == nil {
			size = info.Size()
		}
		got, ok := indexed[id]
		if !ok || got.sha != hex.EncodeToString(sum[:]) || got.captures != size || got.fetches != store.FetchSize(dir) ||
			got.citations != store.CitationsSize(dir) {
			pending = append(pending, id)
		}
	}
	return pending, total, nil
}

// Counts is how many rows each index table holds. Unembedded counts
// passages without a vector from the configured model.
type Counts struct {
	Runs       int64 `json:"runs"`
	Captures   int64 `json:"captures"`
	Sources    int64 `json:"sources"`
	Sightings  int64 `json:"sightings"`
	Fetches    int64 `json:"fetches"`
	Documents  int64 `json:"documents"`
	Passages   int64 `json:"passages"`
	Unembedded int64 `json:"unembedded"`
	// Citations in checked reports, those that don't resolve, and quotes
	// none of their citations hold.
	Citations      int64 `json:"citations"`
	Unresolved     int64 `json:"unresolved_citations"`
	QuotesNotFound int64 `json:"quotes_not_found"`
}

// Counts counts the index's rows.
func (ix *Index) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := ix.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM runs), (SELECT count(*) FROM captures),
		(SELECT count(*) FROM sources), (SELECT count(*) FROM sightings),
		(SELECT count(*) FROM fetches), (SELECT count(*) FROM documents),
		(SELECT count(*) FROM passages),
		(SELECT count(*) FROM passages WHERE `+unembedded+`),
		(SELECT count(*) FROM citations), (SELECT count(*) FROM citations WHERE resolved = false),
		(SELECT count(*) FROM (`+missedQuotes+`) g)`, ix.embedModel).
		Scan(&c.Runs, &c.Captures, &c.Sources, &c.Sightings, &c.Fetches, &c.Documents, &c.Passages, &c.Unembedded,
			&c.Citations, &c.Unresolved, &c.QuotesNotFound)
	return c, err
}

// CitationFailure is a run whose report has citations that don't resolve
// or quotes none of their citations hold.
type CitationFailure struct {
	RunID          string `json:"run_id"`
	Unresolved     int    `json:"unresolved"`
	QuotesNotFound int    `json:"quotes_not_found"`
}

// missedQuotes selects the quotes none of their group's citations hold.
const missedQuotes = `SELECT run_id FROM citations WHERE quote <> '' GROUP BY run_id, group_ord
	HAVING bool_and(quote_status = 'not_found')`

// CitationFailures lists the runs with failed citations, newest first, at
// most limit of them.
func (ix *Index) CitationFailures(ctx context.Context, limit int) ([]CitationFailure, error) {
	rows, err := ix.pool.Query(ctx, `
		WITH unresolved AS (SELECT run_id, count(*) AS n FROM citations WHERE resolved = false GROUP BY run_id),
		missed AS (SELECT run_id, count(*) AS n FROM (`+missedQuotes+`) g GROUP BY run_id)
		SELECT run_id, coalesce(u.n, 0), coalesce(m.n, 0)
		FROM unresolved u FULL JOIN missed m USING (run_id) ORDER BY run_id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[CitationFailure])
}
