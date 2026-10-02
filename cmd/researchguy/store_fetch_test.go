package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
	"github.com/marklubin/researchguy/internal/store/index/indextest"
)

// fetchSite serves an article, a 404 and a fake Ollama /api/embed (down
// when embedDown is set).
func fetchSite(t *testing.T, embedDown *bool) *httptest.Server {
	t.Helper()
	text := strings.Repeat("A paragraph of the fetched article, long enough to be the document. ", 40)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/article":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<html><head><title>Article</title></head><body><article><p>%s</p></article></body></html>`, text)
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
				out[i][1] = 1
			}
			json.NewEncoder(w).Encode(map[string]any{"embeddings": out})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fetchSetup is testSetup with a store block: dsn (if any), fetching on,
// and embedding through srv.
func fetchSetup(t *testing.T, srv *httptest.Server, dsn string) *store.Store {
	t.Helper()
	t.Setenv("RESEARCHGUY_ALLOW_PRIVATE_URLS", "true")
	configDir, _ := testSetup(t)
	f, err := os.OpenFile(filepath.Join(configDir, "config.yaml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(f, "store:\n  dsn: %q\n  fetch:\n    enabled: true\n    timeout: 5s\n  embed:\n    model: nomic-embed-text\n    host: %q\n", dsn, srv.URL)
	f.Close()
	st, err := store.Open(filepath.Join(configDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestStoreFetch_WithoutIndex(t *testing.T) {
	srv := fetchSite(t, nil)
	st := fetchSetup(t, srv, "")
	run := finishedRun(t, st, "t", srv.URL+"/article", srv.URL+"/missing")

	stdout, stderr, err := runCmdStdout(t, "store", "fetch", "--json")
	if err != nil {
		t.Fatalf("store fetch: %v\n%s", err, stderr)
	}
	rep := decode[fetchReport](t, stdout)
	if len(rep.Runs) != 1 || rep.Runs[0].RunID != run.ID() || rep.Runs[0].Fetched != 1 || rep.Runs[0].Failed != 1 || rep.Indexed != nil {
		t.Fatalf("report = %+v", rep)
	}
	if strings.Contains(stdout, "Fetching sources") {
		t.Errorf("--json stdout has progress in it: %q", stdout)
	}
	scan, _ := store.ScanFetches(run.Dir())
	if len(scan.Records) != 2 {
		t.Errorf("fetch log has %d records", len(scan.Records))
	}

	stdout, _, err = runCmdStdout(t, "store", "fetch")
	if err != nil || !strings.Contains(stdout, "No runs waiting on fetching.") {
		t.Fatalf("second fetch: %v %q", err, stdout)
	}
	if _, _, err := runCmdStdout(t, "store", "fetch", "--force"); err == nil || !strings.Contains(err.Error(), "--run") {
		t.Errorf("--force without --run = %v", err)
	}
	stdout, _, err = runCmdStdout(t, "store", "fetch", "--run", run.ID(), "--force")
	if err != nil || !strings.Contains(stdout, run.ID()+": fetched 1 of 2 source(s), 1 failed") {
		t.Fatalf("forced fetch: %v %q", err, stdout)
	}
	if _, _, err := runCmdStdout(t, "store", "fetch", "--run", "../x"); err == nil || !strings.Contains(err.Error(), "invalid run id") {
		t.Errorf("bad --run = %v", err)
	}

	// Doctor lists runs waiting on fetching without calling it a problem.
	finishedRun(t, st, "later", srv.URL+"/article")
	rep2 := decode[doctorReport](t, mustStdout(t, "store", "doctor", "--json"))
	if len(rep2.Unfetched) != 1 || len(rep2.Problems) != 0 {
		t.Errorf("doctor = %+v", rep2)
	}
}

func mustStdout(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, err := runCmdStdout(t, args...)
	if err != nil {
		t.Fatalf("%v: %v\n%s\n%s", args, err, stdout, stderr)
	}
	return stdout
}

func TestStoreFetchAndEmbed_WithIndex(t *testing.T) {
	srv := fetchSite(t, nil)
	st := fetchSetup(t, srv, indextest.DSN(t))
	run := finishedRun(t, st, "t", srv.URL+"/article")
	mustStdout(t, "store", "init")

	rep := decode[fetchReport](t, mustStdout(t, "store", "fetch", "--json"))
	if rep.Indexed == nil || len(rep.Indexed.Ingested) != 1 || rep.Indexed.Ingested[0].RunID != run.ID() || rep.Indexed.Ingested[0].Documents != 1 {
		t.Fatalf("fetch report = %+v", rep.Indexed)
	}
	passages := rep.Indexed.Ingested[0].Passages

	out := mustStdout(t, "store", "doctor")
	if !strings.Contains(out, "1 fetch(es), 1 document(s)") || !strings.Contains(out, fmt.Sprintf("%d passage(s) without a nomic-embed-text vector", passages)) {
		t.Fatalf("doctor before embed: %q", out)
	}

	stats := decode[index.EmbedStats](t, mustStdout(t, "store", "embed", "--json"))
	if stats.Embedded != passages || stats.Remaining != 0 || stats.Model != "nomic-embed-text" {
		t.Fatalf("embed = %+v", stats)
	}
	out = mustStdout(t, "store", "embed")
	if !strings.Contains(out, "Embedded 0 passage(s) with nomic-embed-text, 0 from cache; 0 left") {
		t.Errorf("second embed: %q", out)
	}
	if out := mustStdout(t, "store", "doctor"); strings.Contains(out, "without a") || !strings.Contains(out, "No problems found.") {
		t.Errorf("doctor after embed: %q", out)
	}
	// A rebuild gets every vector from the cache.
	rb := decode[index.SyncStats](t, mustStdout(t, "store", "rebuild", "--json"))
	if len(rb.Ingested) != 1 || rb.Ingested[0].Embedded != passages {
		t.Errorf("rebuild = %+v", rb.Ingested)
	}
}

func TestStoreEmbed_ModelDownFails(t *testing.T) {
	down := true
	srv := fetchSite(t, &down)
	st := fetchSetup(t, srv, indextest.DSN(t))
	finishedRun(t, st, "t", srv.URL+"/article")
	mustStdout(t, "store", "init")
	mustStdout(t, "store", "fetch")
	stdout, _, err := runCmdStdout(t, "store", "embed")
	if err == nil || !strings.Contains(err.Error(), "model not found") || !strings.Contains(stdout, "Embedded 0 passage(s)") {
		t.Fatalf("embed with the model down: %v %q", err, stdout)
	}
	if _, _, err := runCmdStdout(t, "store", "embed", "--budget", "soon"); err == nil || !strings.Contains(err.Error(), "invalid --budget") {
		t.Errorf("bad --budget = %v", err)
	}
}
