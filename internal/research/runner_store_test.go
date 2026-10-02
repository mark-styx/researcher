package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
	"github.com/marklubin/researchguy/internal/store/index/indextest"
)

// evidenceProvider is a single-shot provider that reports raw tool results,
// like the codex and ollama backends.
type evidenceProvider struct {
	mockProvider
	evidence []llm.EvidenceRecord
}

func (e *evidenceProvider) Evidence() []llm.EvidenceRecord { return e.evidence }

func storeConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := testConfig(t)
	cfg.Grepai = config.GrepaiConfig{Binary: "nonexistent-grepai-xyz"}
	cfg.Store.Dir = filepath.Join(t.TempDir(), "store")
	return cfg
}

func TestRun_WritesRunRecordForDive(t *testing.T) {
	cfg := storeConfig(t)
	mock := &mockProvider{response: "the report", metadata: `{"mode":"hybrid"}`}
	out := filepath.Join(t.TempDir(), "report.md")

	res, err := NewRunner(cfg, mock).Run(context.Background(), Task{
		Type: TypeDive, Topic: "topic", NoResearch: true, OutPath: out, Mode: "inquiry", BranchCount: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RunID == "" || res.RunDir != filepath.Join(cfg.Store.Dir, "runs", res.RunID) {
		t.Fatalf("result run id %q dir %q", res.RunID, res.RunDir)
	}
	// The provider got the run, so the hybrid backend can write to it.
	if len(mock.calls) != 1 || mock.calls[0].Run == nil || mock.calls[0].Run.ID() != res.RunID {
		t.Errorf("provider request did not carry run %s", res.RunID)
	}

	rec, err := store.ReadRecord(res.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != store.StatusSucceeded || rec.Kind != TypeDive || rec.Topic != "topic" || rec.Backend != "mock" ||
		rec.Mode != "inquiry" || rec.BranchCount != 3 || rec.ReportPath != out || rec.FinishedAt == nil {
		t.Errorf("run record = %+v", rec)
	}
	var meta map[string]any
	if err := json.Unmarshal(rec.Metadata, &meta); err != nil || meta["mode"] != "hybrid" {
		t.Errorf("run record metadata = %s", rec.Metadata)
	}

	report, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "| Run: "+res.RunID+"*") {
		t.Errorf("report header doesn't name the run:\n%s", report)
	}

	fields := res.Fields("report", "mock")
	if fields["run_id"] != res.RunID || fields["run_dir"] != res.RunDir {
		t.Errorf("Fields() = %v, want run_id and run_dir", fields)
	}
}

func TestRun_FailedTaskStillClosesRecord(t *testing.T) {
	cfg := storeConfig(t)
	mock := &mockProvider{err: errors.New("aggregator failed"), metadata: `{"workers":[]}`}

	res, err := NewRunner(cfg, mock).Run(context.Background(), Task{
		Type: TypeDive, Topic: "topic", NoResearch: true, OutPath: filepath.Join(t.TempDir(), "r.md"),
	})
	if err == nil {
		t.Fatal("want the provider error")
	}
	if res.RunID == "" {
		t.Fatal("a failed task should still say where its run record is")
	}
	rec, rerr := store.ReadRecord(res.RunDir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if rec.Status != store.StatusFailed || !strings.Contains(rec.Error, "aggregator failed") || len(rec.Metadata) == 0 {
		t.Errorf("run record = %+v, want failed with the error and the provider's metadata", rec)
	}
}

func TestRun_SingleShotEvidenceIsCaptured(t *testing.T) {
	cfg := storeConfig(t)
	p := &evidenceProvider{
		mockProvider: mockProvider{response: "answer"},
		evidence: []llm.EvidenceRecord{
			{Label: "web_search: a", Content: "result a"},
			{Label: "web_fetch: https://x", Content: "page x"},
		},
	}
	res, err := NewRunner(cfg, p).Run(context.Background(), Task{Type: TypeAsk, Topic: "q", NoSave: true, NoResearch: true, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	captures, err := store.ReadCaptures(res.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(captures) != 2 || captures[0].Seq != 1 || captures[1].Label != "web_fetch: https://x" || captures[1].Backend != "mock" {
		t.Errorf("captures = %+v", captures)
	}
	if rec, _ := store.ReadRecord(res.RunDir); rec.Captures != 2 || rec.ReportPath != "" {
		t.Errorf("record = %+v, want 2 captures and no report for --no-save", rec)
	}
}

// streamingProvider captures its first record through req.Capture while it
// runs, as codex and ollama do, and leaves the second for the runner.
type streamingProvider struct {
	evidenceProvider
	streamed []llm.EvidenceRecord
}

func (s *streamingProvider) Complete(ctx context.Context, req llm.Request) (string, error) {
	if req.Capture == nil {
		return "", errors.New("runner didn't pass a capture func")
	}
	first := s.evidence[0]
	first.ID = req.Capture(first)
	s.streamed = []llm.EvidenceRecord{first, s.evidence[1]}
	return s.mockProvider.Complete(ctx, req)
}

func (s *streamingProvider) Evidence() []llm.EvidenceRecord { return s.streamed }

func TestRun_SingleShotStreamedEvidenceIsWrittenOnce(t *testing.T) {
	cfg := storeConfig(t)
	p := &streamingProvider{evidenceProvider: evidenceProvider{
		mockProvider: mockProvider{response: "answer"},
		evidence: []llm.EvidenceRecord{
			{Label: "web_search: a", Content: "results", Call: store.Call{Tool: "web_search", Action: "search", Query: "a"}},
			{Label: "web_fetch: https://x", Content: "page x", Call: store.Call{Tool: "web_fetch", Action: "fetch", URL: "https://x"}},
		},
	}}
	res, err := NewRunner(cfg, p).Run(context.Background(), Task{Type: TypeAsk, Topic: "q", NoSave: true, NoResearch: true, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.streamed[0].ID != "E1" {
		t.Errorf("streamed record ID = %q, want E1", p.streamed[0].ID)
	}
	captures, err := store.ReadCaptures(res.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(captures) != 2 || captures[0].Action != "search" || captures[0].Backend != "mock" ||
		captures[1].Seq != 2 || captures[1].URL != "https://x" {
		t.Errorf("captures = %+v, want the streamed one then the other, once each", captures)
	}
}

func TestRun_NoStoreDirRecordsNothing(t *testing.T) {
	cfg := testConfig(t)
	cfg.Grepai = config.GrepaiConfig{Binary: "nonexistent-grepai-xyz"}
	mock := &mockProvider{response: "r"}
	out := filepath.Join(t.TempDir(), "r.md")
	res, err := NewRunner(cfg, mock).Run(context.Background(), Task{Type: TypeDive, Topic: "t", NoResearch: true, OutPath: out})
	if err != nil {
		t.Fatal(err)
	}
	if res.RunID != "" || mock.calls[0].Run != nil {
		t.Errorf("run id %q, request run %v; want none without a store dir", res.RunID, mock.calls[0].Run)
	}
	report, _ := os.ReadFile(out)
	if strings.Contains(string(report), "Run:") {
		t.Errorf("report names a run that doesn't exist:\n%s", report)
	}
	if _, ok := res.Fields("report", "mock")["run_id"]; ok {
		t.Error("Fields() has run_id without a run")
	}
}

func TestRun_UnwritableStoreWarnsAndContinues(t *testing.T) {
	cfg := storeConfig(t)
	// A file where the store directory should be.
	if err := os.WriteFile(cfg.Store.Dir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mock := &mockProvider{response: "r"}
	out := filepath.Join(t.TempDir(), "r.md")
	res, err := NewRunner(cfg, mock).Run(context.Background(), Task{Type: TypeDive, Topic: "t", NoResearch: true, OutPath: out})
	if err != nil {
		t.Fatalf("an unwritable store shouldn't fail the task: %v", err)
	}
	if res.RunID != "" || res.FilePath != out {
		t.Errorf("result = %+v", res)
	}
}

func TestRun_UnknownTypeOpensNoRecord(t *testing.T) {
	cfg := storeConfig(t)
	if _, err := NewRunner(cfg, &mockProvider{}).Run(context.Background(), Task{Type: "nope"}); err == nil {
		t.Fatal("want an error for an unknown type")
	}
	if entries, _ := os.ReadDir(filepath.Join(cfg.Store.Dir, "runs")); len(entries) != 0 {
		t.Errorf("unknown task type created %d run dirs", len(entries))
	}
}

func TestRun_EveryTaskTypeNamesItsRun(t *testing.T) {
	cfg := storeConfig(t)
	src := filepath.Join(t.TempDir(), "src.md")
	if err := os.WriteFile(src, []byte("source doc"), 0o644); err != nil {
		t.Fatal(err)
	}
	src2 := filepath.Join(t.TempDir(), "src2.md")
	if err := os.WriteFile(src2, []byte("other doc"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	tasks := []Task{
		{Type: TypeAsk, Topic: "q", OutPath: filepath.Join(dir, "ask.md"), Quiet: true},
		{Type: TypeWatch, Topic: "w", OutPath: filepath.Join(dir, "watch.md")},
		{Type: TypeReview, Topic: "r", OutPath: filepath.Join(dir, "review.md")},
		{Type: TypeCompare, Topic: "c", Sources: []string{src, src2}, OutPath: filepath.Join(dir, "compare.md")},
		{Type: TypeEnrich, Sources: []string{src}},
	}
	for _, task := range tasks {
		task.NoResearch = true
		res, err := NewRunner(cfg, &mockProvider{response: "body"}).Run(context.Background(), task)
		if err != nil {
			t.Fatalf("%s: %v", task.Type, err)
		}
		data, err := os.ReadFile(res.FilePath)
		if err != nil {
			t.Fatalf("%s: %v", task.Type, err)
		}
		if !strings.Contains(string(data), "Run: "+res.RunID) {
			t.Errorf("%s output doesn't name run %s:\n%s", task.Type, res.RunID, data)
		}
		if rec, err := store.ReadRecord(res.RunDir); err != nil || rec.Kind != task.Type || rec.ReportPath != res.FilePath {
			t.Errorf("%s run record = %+v (%v)", task.Type, rec, err)
		}
	}
}

func TestRun_IndexesTheRunWhenItEnds(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Store.DSN = indextest.DSN(t)
	p := &evidenceProvider{
		mockProvider: mockProvider{response: "answer"},
		evidence: []llm.EvidenceRecord{{Label: "web_fetch", Content: "page",
			Call: store.Call{Tool: "web_fetch", Action: "fetch", URL: "https://example.org/a"}}},
	}
	res, err := NewRunner(cfg, p).Run(context.Background(), Task{Type: TypeAsk, Topic: "q", NoSave: true, NoResearch: true, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Open(context.Background(), cfg.Store.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	c, err := ix.Counts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Runs != 1 || c.Captures != 1 || c.Sources != 1 {
		t.Errorf("index counts = %+v, want the run, its capture and its source", c)
	}
	st, _ := store.Open(cfg.Store.Dir)
	if pending, _, _ := ix.Pending(context.Background(), st); len(pending) != 0 {
		t.Errorf("run %s still pending after the task: %v", res.RunID, pending)
	}
}

func TestRun_UnreachableIndexWarnsAndContinues(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Store.DSN = "postgres://localhost:1/researchguy?connect_timeout=1"
	res, err := NewRunner(cfg, &mockProvider{response: "answer"}).Run(context.Background(),
		Task{Type: TypeAsk, Topic: "q", NoSave: true, NoResearch: true, Quiet: true})
	if err != nil {
		t.Fatalf("an unreachable index shouldn't fail the task: %v", err)
	}
	if rec, err := store.ReadRecord(res.RunDir); err != nil || rec.Status != store.StatusSucceeded {
		t.Errorf("run record = %+v, %v", rec, err)
	}
}

// sourceSite serves a long article and a fake Ollama /api/embed.
func sourceSite(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var embeds atomic.Int32
	text := strings.Repeat("A paragraph of the fetched article, long enough to be the document. ", 40)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/article", "/cited":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<html><head><title>T</title></head><body><article><p>%s</p><p>%s</p></article></body></html>`, text, r.URL.Path)
		case "/api/embed":
			embeds.Add(1)
			var req struct{ Input []string }
			json.NewDecoder(r.Body).Decode(&req)
			out := make([][]float32, len(req.Input))
			for i := range out {
				out[i] = make([]float32, index.Dims)
				out[i][i%index.Dims] = 1
			}
			json.NewEncoder(w).Encode(map[string]any{"embeddings": out})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &embeds
}

func fetchConfig(t *testing.T, srv *httptest.Server) *config.Config {
	t.Helper()
	t.Setenv("RESEARCHGUY_ALLOW_PRIVATE_URLS", "true")
	cfg := storeConfig(t)
	cfg.Store.Fetch = config.StoreFetchConfig{Enabled: true, TopResults: 3, Concurrency: 2, Timeout: "5s", Budget: "30s"}
	cfg.Store.Embed = config.StoreEmbedConfig{Model: "nomic-embed-text", Host: srv.URL}
	return cfg
}

func TestRun_FetchesIndexesAndEmbedsSources(t *testing.T) {
	srv, embeds := sourceSite(t)
	cfg := fetchConfig(t, srv)
	cfg.Store.DSN = indextest.DSN(t)
	p := &evidenceProvider{
		mockProvider: mockProvider{response: "The report cites " + srv.URL + "/cited."},
		evidence: []llm.EvidenceRecord{{Label: "web_search", Content: "results",
			Call: store.Call{Tool: "web_search", Action: "search", Query: "q",
				Results: []store.CaptureResult{{Rank: 1, URL: srv.URL + "/article"}, {Rank: 2, URL: srv.URL + "/missing"}}}}},
	}
	res, err := NewRunner(cfg, p).Run(context.Background(), Task{Type: TypeDive, Topic: "t", NoResearch: true, Quiet: true,
		OutPath: filepath.Join(t.TempDir(), "report.md")})
	if err != nil {
		t.Fatal(err)
	}
	sum, ok, err := store.ReadFetchSummary(res.RunDir)
	if err != nil || !ok || sum.Queued != 3 || sum.Fetched != 2 || sum.Failed != 1 || !sum.Done() {
		t.Errorf("fetch summary = %+v, %v, %v; want the cited page, the article and a failure", sum, ok, err)
	}
	ix, err := index.Open(context.Background(), cfg.Store.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	ix.SetEmbedModel("nomic-embed-text")
	c, _ := ix.Counts(context.Background())
	// The two fetched documents and the run's report.
	if c.Fetches != 3 || c.Documents != 3 || c.Passages < 3 || c.Unembedded != 0 {
		t.Errorf("counts = %+v; want both documents and the report split and embedded", c)
	}
	var origin string
	if err := ix.Pool().QueryRow(context.Background(), `SELECT d.origin FROM runs r JOIN documents d ON d.id = r.report_document_id WHERE r.id = $1`,
		filepath.Base(res.RunDir)).Scan(&origin); err != nil || origin != "synthesis" {
		t.Errorf("report document origin = %q, %v", origin, err)
	}
	if embeds.Load() == 0 {
		t.Error("the embedding model was never called")
	}
}

func TestRun_AskAndNoDSNFetching(t *testing.T) {
	srv, embeds := sourceSite(t)
	cfg := fetchConfig(t, srv)
	p := &evidenceProvider{
		mockProvider: mockProvider{response: "answer"},
		evidence: []llm.EvidenceRecord{{Label: "web_search", Content: "results",
			Call: store.Call{Tool: "web_search", Action: "search", Query: "q", Results: []store.CaptureResult{{Rank: 1, URL: srv.URL + "/article"}}}}},
	}
	// An ask is left for the daemon.
	res, err := NewRunner(cfg, p).Run(context.Background(), Task{Type: TypeAsk, Topic: "q", NoSave: true, NoResearch: true, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.ReadFetchSummary(res.RunDir); ok {
		t.Error("an ask fetched inline")
	}
	st, _ := store.Open(cfg.Store.Dir)
	if pending, _ := st.PendingFetch(); len(pending) != 1 || pending[0] != res.RunID {
		t.Errorf("PendingFetch = %v, want the ask", pending)
	}
	// A dive without an index still fetches; nothing embeds.
	res, err = NewRunner(cfg, p).Run(context.Background(), Task{Type: TypeDive, Topic: "t", NoResearch: true, Quiet: true,
		OutPath: filepath.Join(t.TempDir(), "report.md")})
	if err != nil {
		t.Fatal(err)
	}
	if sum, ok, _ := store.ReadFetchSummary(res.RunDir); !ok || sum.Fetched != 1 {
		t.Errorf("dive without dsn: fetch summary %+v, %v", sum, ok)
	}
	if embeds.Load() != 0 {
		t.Error("embedded without an index")
	}
	// Fetching off: nothing fetched.
	cfg.Store.Fetch.Enabled = false
	res, _ = NewRunner(cfg, p).Run(context.Background(), Task{Type: TypeDive, Topic: "t", NoResearch: true, Quiet: true,
		OutPath: filepath.Join(t.TempDir(), "report2.md")})
	if _, ok, _ := store.ReadFetchSummary(res.RunDir); ok {
		t.Error("fetched with store.fetch.enabled false")
	}
}

func TestRun_EmbeddingDownWarnsAndKeepsIndex(t *testing.T) {
	srv, _ := sourceSite(t)
	cfg := fetchConfig(t, srv)
	cfg.Store.DSN = indextest.DSN(t)
	cfg.Store.Embed.Host = "http://127.0.0.1:1"
	p := &evidenceProvider{
		mockProvider: mockProvider{response: "report"},
		evidence: []llm.EvidenceRecord{{Label: "web_search", Content: "results",
			Call: store.Call{Tool: "web_search", Action: "search", Query: "q", Results: []store.CaptureResult{{Rank: 1, URL: srv.URL + "/article"}}}}},
	}
	if _, err := NewRunner(cfg, p).Run(context.Background(), Task{Type: TypeDive, Topic: "t", NoResearch: true, Quiet: true,
		OutPath: filepath.Join(t.TempDir(), "report.md")}); err != nil {
		t.Fatalf("a down embedding model shouldn't fail the task: %v", err)
	}
	ix, err := index.Open(context.Background(), cfg.Store.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	ix.SetEmbedModel("nomic-embed-text")
	if c, _ := ix.Counts(context.Background()); c.Documents != 2 || c.Passages == 0 || c.Unembedded != c.Passages {
		t.Errorf("counts = %+v; want the document and the report indexed and their passages left for store embed", c)
	}
}
