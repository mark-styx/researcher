package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marklubin/researchguy/internal/config"
	runstore "github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
	"github.com/marklubin/researchguy/internal/store/index/indextest"
)

// sourceSite serves an article and a fake Ollama: /api/embed (embedDown
// makes it fail) and an /api/chat that finds one claim in the article.
func sourceSite(t *testing.T, embedDown *bool) *httptest.Server {
	t.Helper()
	text := strings.Repeat("A paragraph of the fetched article, long enough to be the document. ", 40)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/article":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<html><body><article><p>%s</p></article></body></html>`, text)
		case "/api/embed":
			if embedDown != nil && *embedDown {
				http.Error(w, `{"error":"model not found"}`, http.StatusNotFound)
				return
			}
			var req struct{ Input []string }
			json.NewDecoder(r.Body).Decode(&req)
			out := make([][]float32, len(req.Input))
			for i := range out {
				out[i] = make([]float32, index.Dims)
				out[i][0] = 1
			}
			json.NewEncoder(w).Encode(map[string]any{"embeddings": out})
		case "/api/chat":
			claims, _ := json.Marshal(map[string]any{"claims": []map[string]any{
				{"text": "The article is long.", "quote": "long enough to be the document", "as_of": "", "volatile": false}}})
			json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": string(claims)}})
		case "/api/generate":
			json.NewEncoder(w).Encode(map[string]any{"done": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// searchRun writes a finished run whose one search ranked url first.
func searchRun(t *testing.T, st *runstore.Store, url string) string {
	t.Helper()
	run, err := st.StartRun(runstore.RunRecord{Kind: "ask", Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	run.Append(runstore.Capture{Call: runstore.Call{Tool: "web_search", Action: "search", Query: "q",
		Results: []runstore.CaptureResult{{Rank: 1, URL: url}}}})
	if err := run.Finish(runstore.Finish{Status: runstore.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	return run.ID()
}

func passConfig(t *testing.T, srv *httptest.Server, storeDir, dsn string) *config.Config {
	t.Helper()
	t.Setenv("RESEARCHGUY_ALLOW_PRIVATE_URLS", "true")
	return &config.Config{Store: config.StoreConfig{
		Dir:   storeDir,
		DSN:   dsn,
		Fetch: config.StoreFetchConfig{Enabled: true, TopResults: 3, Concurrency: 2, Timeout: "5s", Budget: "30s"},
		Embed: config.StoreEmbedConfig{Model: "nomic-embed-text", Host: srv.URL, Budget: "30s"},
	}}
}

func TestStorePass_FetchesIndexesAndEmbeds(t *testing.T) {
	srv := sourceSite(t, nil)
	storeDir := filepath.Join(t.TempDir(), "store")
	st, _ := runstore.Open(storeDir)
	id := searchRun(t, st, srv.URL+"/article")
	var logs bytes.Buffer
	s := &Scheduler{cfg: passConfig(t, srv, storeDir, indextest.DSN(t)), logger: log.New(&logs, "", 0)}
	defer func() { s.index.Close() }()

	s.storePass(context.Background())
	out := logs.String()
	if !strings.Contains(out, "Fetched sources for run "+id+": 1 of 1") || !strings.Contains(out, "Indexed 1 run(s)") || !strings.Contains(out, "Embedded ") {
		t.Fatalf("pass log:\n%s", out)
	}
	if pending, _ := st.PendingFetch(); len(pending) != 0 {
		t.Errorf("PendingFetch after the pass = %v", pending)
	}
	c, _ := s.index.Counts(context.Background())
	if c.Documents != 1 || c.Passages == 0 || c.Unembedded != 0 {
		t.Errorf("counts = %+v", c)
	}
	// Nothing new: the next pass logs nothing.
	logs.Reset()
	s.storePass(context.Background())
	if logs.Len() != 0 {
		t.Errorf("idle pass logged:\n%s", logs.String())
	}
}

func TestFetchPending_BoundedAndSkipsBusy(t *testing.T) {
	srv := sourceSite(t, nil)
	storeDir := filepath.Join(t.TempDir(), "store")
	st, _ := runstore.Open(storeDir)
	var ids []string
	for i := 0; i < maxFetchRuns+2; i++ {
		ids = append(ids, searchRun(t, st, srv.URL+"/article"))
	}
	// One run is being fetched by another process.
	busy, err := runstore.OpenFetchLog(st.RunDir(ids[len(ids)-1]))
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	var logs bytes.Buffer
	s := &Scheduler{cfg: passConfig(t, srv, storeDir, ""), logger: log.New(&logs, "", 0)}
	s.fetchPending(context.Background())
	if n := strings.Count(logs.String(), "Fetched sources for run"); n != maxFetchRuns {
		t.Fatalf("fetched %d runs, want %d:\n%s", n, maxFetchRuns, logs.String())
	}
	pending, _ := st.PendingFetch()
	if len(pending) != 2 || !slices.Contains(pending, ids[len(ids)-1]) {
		t.Errorf("pending = %v; want one past the bound and the busy one", pending)
	}
	if strings.Contains(logs.String(), "error") {
		t.Errorf("a busy run was logged as a problem:\n%s", logs.String())
	}
}

func TestEmbedPending_DownModelLoggedOnceAndRecovers(t *testing.T) {
	down := true
	srv := sourceSite(t, &down)
	storeDir := filepath.Join(t.TempDir(), "store")
	st, _ := runstore.Open(storeDir)
	searchRun(t, st, srv.URL+"/article")
	var logs bytes.Buffer
	s := &Scheduler{cfg: passConfig(t, srv, storeDir, indextest.DSN(t)), logger: log.New(&logs, "", 0)}
	defer func() { s.index.Close() }()
	s.storePass(context.Background())
	s.storePass(context.Background())
	if n := strings.Count(logs.String(), "Embedding error"); n != 1 {
		t.Fatalf("down model logged %d times, want once:\n%s", n, logs.String())
	}
	down = false
	s.storePass(context.Background())
	if !strings.Contains(logs.String(), "Embedding recovered") || !strings.Contains(logs.String(), "Embedded ") {
		t.Errorf("after recovery:\n%s", logs.String())
	}
}

func TestEmbedPending_BudgetIsNotAProblem(t *testing.T) {
	srv := sourceSite(t, nil)
	storeDir := filepath.Join(t.TempDir(), "store")
	st, _ := runstore.Open(storeDir)
	searchRun(t, st, srv.URL+"/article")
	var logs bytes.Buffer
	cfg := passConfig(t, srv, storeDir, indextest.DSN(t))
	s := &Scheduler{cfg: cfg, logger: log.New(&logs, "", 0)}
	defer func() { s.index.Close() }()
	s.fetchPending(context.Background())
	s.syncIndex(context.Background())
	cfg.Store.Embed.Budget = "1ns"
	s.embedPending(context.Background())
	if strings.Contains(logs.String(), "Embedding error") {
		t.Errorf("running out of budget was logged as a problem:\n%s", logs.String())
	}
}

func TestRun_StorePassRunsWithoutAnIndex(t *testing.T) {
	srv := sourceSite(t, nil)
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)
	storeDir := filepath.Join(dir, "store")
	st, _ := runstore.Open(storeDir)
	id := searchRun(t, st, srv.URL+"/article")
	logFile := filepath.Join(dir, "sched.log")
	cfg := passConfig(t, srv, storeDir, "")
	cfg.Scheduler = config.SchedulerConfig{PollInterval: "50ms", MaxConcurrent: 1, LogFile: logFile, PIDFile: filepath.Join(dir, "sched.pid")}
	sched, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		sched.Run()
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(logFile)
		if strings.Contains(string(data), "Fetched sources for run "+id) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon never fetched; log:\n%s", data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	sched.Stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop didn't end the run loop")
	}
	if data, _ := os.ReadFile(logFile); strings.Contains(string(data), "Store index sync enabled") {
		t.Error("index sync enabled with no dsn")
	}
}

func TestExtractClaims_YieldsToTasks(t *testing.T) {
	srv := sourceSite(t, nil)
	storeDir := filepath.Join(t.TempDir(), "store")
	st, _ := runstore.Open(storeDir)
	searchRun(t, st, srv.URL+"/article")
	var logs bytes.Buffer
	cfg := passConfig(t, srv, storeDir, indextest.DSN(t))
	cfg.Store.Claims = config.StoreClaimsConfig{Enabled: true, Model: "qwen-test", Host: srv.URL, Budget: "30s", ChunkChars: 6000, MaxChunks: 8, MaxAttempts: 3}
	s := &Scheduler{cfg: cfg, logger: log.New(&logs, "", 0), sem: make(chan struct{}, 1)}
	defer func() { s.index.Close() }()

	// A task holds a slot: the pass indexes but doesn't extract.
	s.sem <- struct{}{}
	s.storePass(context.Background())
	if out := logs.String(); !strings.Contains(out, "Embedded ") || strings.Contains(out, "Extracted") {
		t.Fatalf("pass with a task running:\n%s", out)
	}
	if files, _ := st.Extractions(); len(files) != 0 {
		t.Fatalf("extracted while a task ran: %d files", len(files))
	}
	<-s.sem
	s.storePass(context.Background())
	if out := logs.String(); !strings.Contains(out, "Extracted 1 claim(s) from 1 text(s) with qwen-test (0 left, 0 with failed chunks)") {
		t.Fatalf("pass log:\n%s", out)
	}
	c, _ := s.index.ClaimCandidates(context.Background(), runstore.ExtractorDir("qwen-test/claims-v1"))
	if len(c) != 0 {
		t.Errorf("still waiting: %+v", c)
	}
	// Disabled, it doesn't run.
	cfg.Store.Claims.Enabled = false
	logs.Reset()
	searchRun(t, st, srv.URL+"/article?second")
	s.storePass(context.Background())
	if strings.Contains(logs.String(), "Extracted") {
		t.Errorf("disabled extraction ran:\n%s", logs.String())
	}
}
