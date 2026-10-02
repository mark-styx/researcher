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
	var stats SyncStats
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
	var stats SyncStats
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
		if _, err := tx.Exec(ctx, `TRUNCATE sightings, captures, sources, runs`); err != nil {
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
			rs, err := ingestSnapshot(ctx, tx, snap, true)
			if err != nil {
				return err
			}
			stats.Ingested = append(stats.Ingested, rs)
		}
		return nil
	})
	if err != nil {
		return SyncStats{Interrupted: marked}, err
	}
	return stats, nil
}

// Pending lists the runs in st that the index doesn't have, or has as they
// were before their record or capture log last changed, and how many runs
// with a record the store holds. Run dirs without a run.json are skipped;
// store.Check reports them.
func (ix *Index) Pending(ctx context.Context, st *store.Store) ([]string, int, error) {
	indexed := map[string][2]any{}
	rows, err := ix.pool.Query(ctx, `SELECT id, record_sha256, capture_bytes FROM runs`)
	if err != nil {
		return nil, 0, fmt.Errorf("listing indexed runs: %w", err)
	}
	for rows.Next() {
		var id, sha string
		var size int64
		if err := rows.Scan(&id, &sha, &size); err != nil {
			rows.Close()
			return nil, 0, err
		}
		indexed[id] = [2]any{sha, size}
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
		if !ok || got[0] != hex.EncodeToString(sum[:]) || got[1] != size {
			pending = append(pending, id)
		}
	}
	return pending, total, nil
}

// Counts is how many rows each index table holds.
type Counts struct {
	Runs      int64 `json:"runs"`
	Captures  int64 `json:"captures"`
	Sources   int64 `json:"sources"`
	Sightings int64 `json:"sightings"`
}

// Counts counts the index's rows.
func (ix *Index) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := ix.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM runs), (SELECT count(*) FROM captures),
		(SELECT count(*) FROM sources), (SELECT count(*) FROM sightings)`).
		Scan(&c.Runs, &c.Captures, &c.Sources, &c.Sightings)
	return c, err
}
