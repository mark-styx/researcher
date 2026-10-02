package index

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index/indextest"
)

func openIndex(t *testing.T) (*Index, string) {
	t.Helper()
	dsn := indextest.DSN(t)
	ix, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ix.Close)
	return ix, dsn
}

// sampleRun writes a finished run with a search, an opened page and a
// fetch, the way the hybrid and ollama paths record them.
func sampleRun(t *testing.T, st *store.Store) *store.Run {
	t.Helper()
	run, err := st.StartRun(store.RunRecord{Kind: "dive", Topic: "go generics", Backend: "hybrid", Mode: "inquiry"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Append(
		store.Capture{Worker: 1, Shard: "core", Backend: "codex", Model: "m", Label: "web_search: go generics", Content: "results",
			Call: store.Call{Tool: "web_search", Action: "search", Query: "go generics", Results: []store.CaptureResult{
				{Rank: 1, Title: "Go 1.18 is released", URL: "https://go.dev/blog/go1.18?utm_source=openai", Snippet: "generics"},
				{Rank: 2, Title: "Paper", URL: "https://doi.org/10.1145/ABC.123", Snippet: "type theory"},
				{Rank: 3, Title: "Bad", URL: "ftp://example.org/x"},
			}}},
		store.Capture{Worker: 1, Shard: "core", Backend: "codex", Label: "web_search open: https://go.dev/blog/go1.18", Content: "page",
			Call: store.Call{Tool: "web_search", Action: "open", URL: "https://go.dev/blog/go1.18",
				Results: []store.CaptureResult{{Opened: true, Title: "Go 1.18 is released", URL: "https://www.go.dev/blog/go1.18/", RefID: "turn1view0"}}}},
		store.Capture{Worker: 2, Shard: "recent", Backend: "ollama", Label: "web_fetch", Content: "page text",
			Call: store.Call{Tool: "web_fetch", Action: "fetch", URL: "https://example.org/post"}},
	); err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(store.Finish{Status: store.StatusSucceeded, ReportPath: "/r/report.md", Metadata: `{"mode":"inquiry"}`}); err != nil {
		t.Fatal(err)
	}
	return run
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestOpen_MigratesAndIsIdempotent(t *testing.T) {
	ix, dsn := openIndex(t)
	ctx := context.Background()
	v, err := ix.SchemaVersion(ctx)
	if err != nil || v != LatestVersion() || v < 1 {
		t.Fatalf("schema version = %d, %v; want %d", v, err, LatestVersion())
	}
	again, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer again.Close()
	var applied int
	if err := again.pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil || applied != LatestVersion() {
		t.Errorf("schema_migrations rows = %d, %v; each migration once", applied, err)
	}
}

func TestOpen_ConcurrentMigrations(t *testing.T) {
	dsn := indextest.DSN(t)
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			ix, err := Open(context.Background(), dsn)
			if err == nil {
				ix.Close()
			}
			errs <- err
		}()
	}
	for i := 0; i < 4; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent Open: %v", err)
		}
	}
}

func TestOpen_MissingDatabaseAndBadDSN(t *testing.T) {
	dsn := indextest.DSN(t)
	cfg, _ := pgx.ParseConfig(dsn)
	missing := indextest.WithDatabase(t, dsn, cfg.Database+"_missing")
	if _, err := Open(context.Background(), missing); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("Open on a missing database = %v, want ErrNoDatabase", err)
	}
	if _, err := Open(context.Background(), ""); err == nil {
		t.Error("Open(\"\") should fail")
	}
	if _, err := Open(context.Background(), "postgres://localhost:1/x?connect_timeout=1"); err == nil {
		t.Error("Open on a closed port should fail")
	}
}

func TestCreateDatabase(t *testing.T) {
	dsn := indextest.DSN(t)
	cfg, _ := pgx.ParseConfig(dsn)
	fresh := indextest.WithDatabase(t, dsn, cfg.Database+"_new")
	t.Cleanup(func() { indextest.Drop(t, cfg.Database+"_new") })
	ctx := context.Background()
	created, err := CreateDatabase(ctx, fresh)
	if err != nil || !created {
		t.Fatalf("CreateDatabase = %v, %v; want created", created, err)
	}
	if created, err := CreateDatabase(ctx, fresh); err != nil || created {
		t.Errorf("second CreateDatabase = %v, %v; want already there", created, err)
	}
	ix, err := Open(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	ix.Close()
	if _, err := CreateDatabase(ctx, indextest.WithDatabase(t, dsn, "postgres")); err == nil {
		t.Error("CreateDatabase should refuse the postgres database")
	}
}

func TestIngestRun_RunCapturesSourcesSightings(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	run := sampleRun(t, st)

	stats, err := ix.IngestRun(ctx, run.Dir(), false)
	if err != nil {
		t.Fatal(err)
	}
	// go.dev's search result and opened page are one source; the doi and
	// the fetched page are two more; the ftp URL isn't a source.
	if stats.Captures != 3 || stats.Sources != 3 || stats.Sightings != 4 || stats.BadURLs != 1 || stats.Skipped {
		t.Errorf("stats = %+v", stats)
	}

	var status, kind, mode string
	var meta []byte
	if err := ix.pool.QueryRow(ctx, `SELECT status, kind, mode, meta FROM runs WHERE id = $1`, run.ID()).Scan(&status, &kind, &mode, &meta); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || kind != "dive" || mode != "inquiry" || !strings.Contains(string(meta), `"inquiry"`) {
		t.Errorf("run row = %s %s %s %s", status, kind, mode, meta)
	}

	var action, url string
	var payloadLabel string
	if err := ix.pool.QueryRow(ctx, `SELECT action, url, payload->>'label' FROM captures WHERE run_id = $1 AND seq = 2`, run.ID()).Scan(&action, &url, &payloadLabel); err != nil {
		t.Fatal(err)
	}
	if action != "open" || url != "https://go.dev/blog/go1.18" || payloadLabel != "web_search open: https://go.dev/blog/go1.18" {
		t.Errorf("capture 2 = %s %s %q", action, url, payloadLabel)
	}

	var id int64
	var domain, title string
	var doi *string
	if err := ix.pool.QueryRow(ctx, `SELECT id, domain, title, doi FROM sources WHERE url_key = 'go.dev/blog/go1.18'`).Scan(&id, &domain, &title, &doi); err != nil {
		t.Fatal(err)
	}
	if id != SourceID("go.dev/blog/go1.18") || domain != "go.dev" || title != "Go 1.18 is released" || doi != nil {
		t.Errorf("go.dev source = %d %s %q %v", id, domain, title, doi)
	}
	if err := ix.pool.QueryRow(ctx, `SELECT doi FROM sources WHERE domain = 'doi.org'`).Scan(&doi); err != nil || doi == nil || *doi != "10.1145/abc.123" {
		t.Errorf("doi source doi = %v, %v", doi, err)
	}

	rows, err := ix.pool.Query(ctx, `SELECT s.seq, s.role, s.rank, src.url_key FROM sightings s JOIN sources src ON src.id = s.source_id
		WHERE s.run_id = $1 ORDER BY s.seq, s.ord`, run.ID())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var seq int
		var role, key string
		var rank *int
		if err := rows.Scan(&seq, &role, &rank, &key); err != nil {
			t.Fatal(err)
		}
		r := "-"
		if rank != nil {
			r = string(rune('0' + *rank))
		}
		got = append(got, strings.Join([]string{string(rune('0' + seq)), role, r, key}, " "))
	}
	want := []string{
		"1 result 1 go.dev/blog/go1.18",
		"1 result 2 doi.org/10.1145/ABC.123",
		"2 opened - go.dev/blog/go1.18",
		"3 fetched - example.org/post",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("sightings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestIngestRun_IdempotentAndPicksUpChanges(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	run, err := st.StartRun(store.RunRecord{Kind: "dive", Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	run.Append(store.Capture{Label: "a", Content: "1", Call: store.Call{Tool: "web_fetch", Action: "fetch", URL: "https://a.test/"}})

	// Mid-run: the record says running.
	if s, err := ix.IngestRun(ctx, run.Dir(), false); err != nil || s.Skipped || s.Captures != 1 {
		t.Fatalf("first ingest = %+v, %v", s, err)
	}
	if s, err := ix.IngestRun(ctx, run.Dir(), false); err != nil || !s.Skipped {
		t.Errorf("unchanged run ingest = %+v, %v; want skipped", s, err)
	}

	run.Append(store.Capture{Label: "b", Content: "2", Call: store.Call{Tool: "web_fetch", Action: "fetch", URL: "https://a.test/"}})
	run.Finish(store.Finish{Status: store.StatusSucceeded})
	if s, err := ix.IngestRun(ctx, run.Dir(), false); err != nil || s.Skipped || s.Captures != 2 {
		t.Fatalf("ingest after changes = %+v, %v", s, err)
	}
	if s, err := ix.IngestRun(ctx, run.Dir(), true); err != nil || s.Skipped {
		t.Errorf("forced ingest = %+v, %v", s, err)
	}
	c, err := ix.Counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.Runs != 1 || c.Captures != 2 || c.Sources != 1 || c.Sightings != 2 {
		t.Errorf("counts = %+v, want each row once", c)
	}
	var status string
	var first, last time.Time
	ix.pool.QueryRow(ctx, `SELECT status FROM runs`).Scan(&status)
	ix.pool.QueryRow(ctx, `SELECT first_seen_at, last_seen_at FROM sources`).Scan(&first, &last)
	if status != "succeeded" || last.Before(first) {
		t.Errorf("status %s, source seen %v..%v", status, first, last)
	}
}

func TestIngestRun_PartialAndBadLines(t *testing.T) {
	ix, _ := openIndex(t)
	st := newStore(t)
	run, _ := st.StartRun(store.RunRecord{Kind: "ask"})
	run.Append(store.Capture{Label: "a", Content: "1"})
	f, _ := os.OpenFile(filepath.Join(run.Dir(), "captures.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("{broken\n{\"seq\":9,\"lab")
	f.Close()
	s, err := ix.IngestRun(context.Background(), run.Dir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if s.Captures != 1 || !s.Partial || len(s.BadLines) != 1 || s.BadLines[0] != 2 {
		t.Errorf("stats = %+v, want 1 capture, line 2 bad, partial last line", s)
	}
	if _, err := ix.IngestRun(context.Background(), t.TempDir(), false); err == nil {
		t.Error("ingesting a dir with no run.json should fail")
	}
}

func TestSync_ReconcilesAndIngestsPending(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	a := sampleRun(t, st)
	b := sampleRun(t, st)
	if _, err := ix.IngestRun(ctx, a.Dir(), false); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(st.RunDir("20000101T000000Z-000000"), 0o755); err != nil { // no run.json
		t.Fatal(err)
	}

	pending, total, err := ix.Pending(ctx, st)
	if err != nil || len(pending) != 1 || pending[0] != b.ID() || total != 2 {
		t.Fatalf("Pending = %v, %d, %v; want [%s] of 2", pending, total, err, b.ID())
	}
	stats, err := ix.Sync(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Ingested) != 1 || stats.Ingested[0].RunID != b.ID() || stats.UpToDate != 1 || len(stats.Failed) != 0 {
		t.Errorf("Sync = %+v", stats)
	}
	if pending, _, _ := ix.Pending(ctx, st); len(pending) != 0 {
		t.Errorf("pending after sync = %v", pending)
	}
	// Both runs saw the same sources: still one row each.
	if c, _ := ix.Counts(ctx); c.Runs != 2 || c.Captures != 6 || c.Sources != 3 || c.Sightings != 8 {
		t.Errorf("counts = %+v", c)
	}
}

func TestRebuild_RecreatesTheSameIndex(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	run := sampleRun(t, st)
	sampleRun(t, st)
	if _, err := ix.Sync(ctx, st); err != nil {
		t.Fatal(err)
	}
	before, _ := ix.Counts(ctx)
	// A row the store doesn't back is gone after a rebuild.
	if _, err := ix.pool.Exec(ctx, `UPDATE runs SET topic = 'edited' WHERE id = $1`, run.ID()); err != nil {
		t.Fatal(err)
	}
	stats, err := ix.Rebuild(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := ix.Counts(ctx)
	if len(stats.Ingested) != 2 || after != before {
		t.Errorf("rebuild ingested %d, counts %+v, want 2 and %+v", len(stats.Ingested), after, before)
	}
	var topic string
	ix.pool.QueryRow(ctx, `SELECT topic FROM runs WHERE id = $1`, run.ID()).Scan(&topic)
	if topic != "go generics" {
		t.Errorf("topic after rebuild = %q, want the store's", topic)
	}
	var id int64
	ix.pool.QueryRow(ctx, `SELECT id FROM sources WHERE url_key = 'example.org/post'`).Scan(&id)
	if id != SourceID("example.org/post") {
		t.Errorf("source id changed across rebuild: %d", id)
	}
}

func TestSourceID_StableAndPositive(t *testing.T) {
	a, b := SourceID("go.dev/blog/go1.18"), SourceID("go.dev/blog/go1.18")
	if a != b || a <= 0 || SourceID("other.test") == a {
		t.Errorf("SourceID = %d, %d", a, b)
	}
}

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"postgres://mark:secret@localhost:5432/researchguy": "postgres://mark:%2A%2A%2A@localhost:5432/researchguy",
		"postgres://localhost:5432/researchguy":             "postgres://localhost:5432/researchguy",
		"host=localhost password=secret dbname=x":           "host=localhost password=*** dbname=x",
		"host=localhost password='a b' dbname=x":            "host=localhost password=*** dbname=x",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
		if strings.Contains(Redact(in), "secret") {
			t.Errorf("Redact(%q) leaks the password", in)
		}
	}
}

func TestDoiOf(t *testing.T) {
	cases := map[string]string{
		"https://doi.org/10.1177/0956797619856844": "10.1177/0956797619856844",
		"http://dx.doi.org/10.1000/ABC":            "10.1000/abc",
		"https://doi.org/":                         "",
		"https://example.org/10.1000/abc":          "",
	}
	for in, want := range cases {
		got := doiOf(in)
		if (got == nil && want != "") || (got != nil && *got != want) {
			t.Errorf("doiOf(%q) = %v, want %q", in, got, want)
		}
	}
}
