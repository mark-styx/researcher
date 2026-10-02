package index

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marklubin/researchguy/internal/graph"
	"github.com/marklubin/researchguy/internal/store"
)

// batchSize caps the statements sent in one round trip.
const batchSize = 1000

// RunStats is what ingesting one run did.
type RunStats struct {
	RunID     string `json:"run_id"`
	Skipped   bool   `json:"skipped,omitempty"` // already up to date
	Captures  int    `json:"captures"`
	Sources   int    `json:"sources"`
	Sightings int    `json:"sightings"`
	// BadURLs are result URLs that aren't http(s) and so aren't sources.
	BadURLs  int   `json:"bad_urls,omitempty"`
	BadLines []int `json:"bad_lines,omitempty"`
	Partial  bool  `json:"partial,omitempty"`
	// Fetches are fetch log records; Documents and Passages count those
	// new to the index, Embedded the new passages whose vector was cached.
	Fetches       int   `json:"fetches"`
	Documents     int   `json:"documents"`
	Passages      int   `json:"passages"`
	Embedded      int   `json:"embedded"`
	FetchBadLines []int `json:"fetch_bad_lines,omitempty"`
	MissingTexts  int   `json:"missing_texts,omitempty"`
}

// snapshot is a run directory as read for ingest.
type snapshot struct {
	rec       store.RunRecord
	recordSHA string
	log       store.CaptureLog
	fetches   store.FetchLogScan
	store     *store.Store
}

func readSnapshot(dir string) (snapshot, error) {
	data, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		return snapshot{}, fmt.Errorf("reading run record: %w", err)
	}
	var snap snapshot
	if err := json.Unmarshal(data, &snap.rec); err != nil {
		return snapshot{}, fmt.Errorf("decoding run record: %w", err)
	}
	if snap.rec.ID == "" {
		snap.rec.ID = filepath.Base(dir)
	}
	sum := sha256.Sum256(data)
	snap.recordSHA = hex.EncodeToString(sum[:])
	if snap.log, err = store.ScanCaptures(dir); err != nil {
		return snapshot{}, err
	}
	if snap.fetches, err = store.ScanFetches(dir); err != nil {
		return snapshot{}, err
	}
	snap.store = store.OfRunDir(dir)
	return snap, nil
}

// IngestRun indexes one run directory. Ingest is idempotent: captures and
// sightings are keyed by (run, seq), fetches by (run, seq), documents by
// source and text, and a run whose record, capture log and fetch log
// haven't changed since it was last ingested is skipped unless force is
// set.
func (ix *Index) IngestRun(ctx context.Context, dir string, force bool) (RunStats, error) {
	snap, err := readSnapshot(dir)
	if err != nil {
		return RunStats{RunID: filepath.Base(dir)}, err
	}
	var stats RunStats
	err = pgx.BeginFunc(ctx, ix.pool, func(tx pgx.Tx) error {
		var e error
		stats, e = ix.ingestSnapshot(ctx, tx, snap, force)
		return e
	})
	return stats, err
}

func (ix *Index) ingestSnapshot(ctx context.Context, tx pgx.Tx, snap snapshot, force bool) (RunStats, error) {
	rec := snap.rec
	stats := RunStats{RunID: rec.ID, Captures: len(snap.log.Captures), BadLines: snap.log.BadLines, Partial: snap.log.Partial,
		Fetches: len(snap.fetches.Records), FetchBadLines: snap.fetches.BadLines}

	// Two processes ingesting one run (the runner when it finishes and the
	// daemon catching up) take turns.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, rec.ID); err != nil {
		return stats, fmt.Errorf("locking run %s: %w", rec.ID, err)
	}
	if !force {
		var sha string
		var size, fetchSize int64
		err := tx.QueryRow(ctx, `SELECT record_sha256, capture_bytes, fetch_bytes FROM runs WHERE id = $1`, rec.ID).Scan(&sha, &size, &fetchSize)
		if err == nil && sha == snap.recordSHA && size == snap.log.Size && fetchSize == snap.fetches.Size {
			stats.Skipped = true
			return stats, nil
		}
		if err != nil && err != pgx.ErrNoRows {
			return stats, fmt.Errorf("reading run %s: %w", rec.ID, err)
		}
	}

	var meta any
	if len(rec.Metadata) > 0 {
		meta = string(rec.Metadata)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO runs (id, kind, topic, mode, backend, branch_count, status, error,
			started_at, finished_at, report_path, meta, record_sha256, capture_bytes, fetch_bytes, ingested_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13, $14, $15, now())
		ON CONFLICT (id) DO UPDATE SET
			kind = EXCLUDED.kind, topic = EXCLUDED.topic, mode = EXCLUDED.mode,
			backend = EXCLUDED.backend, branch_count = EXCLUDED.branch_count,
			status = EXCLUDED.status, error = EXCLUDED.error,
			started_at = EXCLUDED.started_at, finished_at = EXCLUDED.finished_at,
			report_path = EXCLUDED.report_path, meta = EXCLUDED.meta,
			record_sha256 = EXCLUDED.record_sha256, capture_bytes = EXCLUDED.capture_bytes,
			fetch_bytes = EXCLUDED.fetch_bytes, ingested_at = EXCLUDED.ingested_at`,
		rec.ID, rec.Kind, rec.Topic, rec.Mode, rec.Backend, rec.BranchCount, rec.Status, rec.Error,
		rec.StartedAt, rec.FinishedAt, rec.ReportPath, meta, snap.recordSHA, snap.log.Size, snap.fetches.Size,
	); err != nil {
		return stats, fmt.Errorf("writing run %s: %w", rec.ID, err)
	}

	var captures []queued
	for _, c := range snap.log.Captures {
		payload, err := json.Marshal(c)
		if err != nil {
			return stats, fmt.Errorf("encoding capture %d: %w", c.Seq, err)
		}
		captures = append(captures, queued{`
			INSERT INTO captures (run_id, seq, captured_at, worker, shard, backend, model,
				tool, action, query, url, label, payload)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (run_id, seq) DO NOTHING`,
			[]any{rec.ID, c.Seq, c.CapturedAt, c.Worker, c.Shard, c.Backend, c.Model,
				c.Tool, c.Action, c.Query, c.URL, c.Label, payload}})
	}
	if err := sendAll(ctx, tx, captures); err != nil {
		return stats, fmt.Errorf("writing captures for run %s: %w", rec.ID, err)
	}

	sources, sightings, bad := sightingsOf(snap.log.Captures)
	addFetchSources(sources, snap.fetches.Records)
	stats.BadURLs = bad
	stats.Sources = len(sources)
	stats.Sightings = len(sightings)
	var q []queued
	// Sorted keys give every ingest the same row lock order, so concurrent
	// ingests of runs that share sources can't deadlock.
	keys := make([]string, 0, len(sources))
	for k := range sources {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		s := sources[k]
		q = append(q, queued{`
			INSERT INTO sources (id, url_key, url, domain, title, kind, doi, first_seen_at, last_seen_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (url_key) DO UPDATE SET
				title = CASE WHEN sources.title = '' THEN EXCLUDED.title ELSE sources.title END,
				kind = CASE WHEN sources.kind = 'web' THEN EXCLUDED.kind ELSE sources.kind END,
				doi = COALESCE(sources.doi, EXCLUDED.doi),
				first_seen_at = LEAST(sources.first_seen_at, EXCLUDED.first_seen_at),
				last_seen_at = GREATEST(sources.last_seen_at, EXCLUDED.last_seen_at)`,
			[]any{SourceID(k), k, s.url, s.domain, s.title, sourceKind(k, s.doi), s.doi, s.first, s.last}})
	}
	for _, s := range sightings {
		q = append(q, queued{`
			INSERT INTO sightings (run_id, seq, ord, source_id, role, rank, title, snippet)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (run_id, seq, ord) DO NOTHING`,
			[]any{rec.ID, s.seq, s.ord, SourceID(s.key), s.role, s.rank, s.title, s.snippet}})
	}
	if err := sendAll(ctx, tx, q); err != nil {
		return stats, fmt.Errorf("writing sources for run %s: %w", rec.ID, err)
	}
	if err := ix.ingestFetches(ctx, tx, snap, &stats); err != nil {
		return stats, fmt.Errorf("writing fetches for run %s: %w", rec.ID, err)
	}
	return stats, nil
}

type queued struct {
	sql  string
	args []any
}

// sendAll executes statements in batches of batchSize.
func sendAll(ctx context.Context, tx pgx.Tx, qs []queued) error {
	for start := 0; start < len(qs); start += batchSize {
		end := min(start+batchSize, len(qs))
		b := &pgx.Batch{}
		for _, q := range qs[start:end] {
			b.Queue(q.sql, q.args...)
		}
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return err
		}
	}
	return nil
}

// SourceID is a source's id: the first 63 bits of the SHA-256 of its
// url_key, so it is stable across rebuilds and ingest order.
func SourceID(urlKey string) int64 {
	sum := sha256.Sum256([]byte(urlKey))
	return int64(binary.BigEndian.Uint64(sum[:8]) &^ (1 << 63))
}

type sourceRow struct {
	url, domain, title string
	doi                *string
	first, last        time.Time
}

type sighting struct {
	key, role, title, snippet string
	seq, ord                  int
	rank                      *int
}

// sightingsOf lists every source the captures saw, keyed by url_key, and
// each sighting of one. It returns how many URLs weren't http(s).
func sightingsOf(captures []store.Capture) (map[string]*sourceRow, []sighting, int) {
	sources := map[string]*sourceRow{}
	var out []sighting
	bad := 0
	see := func(c store.Capture, ord int, raw, title, snippet, role string, rank int) bool {
		key, err := graph.NormalizeURL(raw)
		if err != nil {
			bad++
			return false
		}
		s := sighting{key: key, role: role, title: title, snippet: snippet, seq: c.Seq, ord: ord}
		if rank > 0 {
			s.rank = &rank
		}
		out = append(out, s)
		row, ok := sources[key]
		if !ok {
			row = &sourceRow{url: strings.TrimSpace(raw), domain: domainOf(key), doi: doiOf(raw), first: c.CapturedAt, last: c.CapturedAt}
			sources[key] = row
		}
		if row.title == "" {
			row.title = title
		}
		if c.CapturedAt.Before(row.first) {
			row.first = c.CapturedAt
		}
		if c.CapturedAt.After(row.last) {
			row.last = c.CapturedAt
		}
		return true
	}
	for _, c := range captures {
		ord := 0
		pageSeen := false
		pageKey, _ := graph.NormalizeURL(c.URL)
		for _, r := range c.Results {
			role := "result"
			if r.Opened {
				role = "opened"
			}
			if see(c, ord, r.URL, r.Title, r.Snippet, role, r.Rank) {
				ord++
				if k, _ := graph.NormalizeURL(r.URL); k != "" && k == pageKey {
					pageSeen = true
				}
			}
		}
		// A fetched page, or an opened one its results don't list.
		if c.URL != "" && !pageSeen {
			role := "opened"
			if c.Action == "fetch" {
				role = "fetched"
			}
			see(c, ord, c.URL, "", "", role, 0)
		}
	}
	return sources, out, bad
}

// domainOf is the host part of a url_key.
func domainOf(key string) string {
	if i := strings.IndexAny(key, "/?"); i >= 0 {
		return key[:i]
	}
	return key
}

// doiOf returns the DOI of a doi.org link, lowercased (DOIs are case
// insensitive), or nil.
func doiOf(raw string) *string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host != "doi.org" && host != "dx.doi.org" {
		return nil
	}
	doi := strings.ToLower(strings.Trim(u.Path, "/"))
	if !strings.HasPrefix(doi, "10.") {
		return nil
	}
	return &doi
}
