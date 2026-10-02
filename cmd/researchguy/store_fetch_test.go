package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marklubin/researchguy/internal/claims"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
	"github.com/marklubin/researchguy/internal/store/index/indextest"
	"github.com/marklubin/researchguy/internal/store/retrieve"
)

// fetchSite serves an article, a 404 and a fake Ollama: /api/embed (down
// when embedDown is set) and an /api/chat that finds one claim in the
// article.
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
	fmt.Fprintf(f, "store:\n  dsn: %q\n  fetch:\n    enabled: true\n    timeout: 5s\n  embed:\n    model: nomic-embed-text\n    host: %q\n  claims:\n    model: qwen-test\n    host: %q\n    link:\n      backend: ollama\n      model: qwen-test\n", dsn, srv.URL, srv.URL)
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

func TestStoreExtract_WithIndex(t *testing.T) {
	srv := fetchSite(t, nil)
	dsn := indextest.DSN(t)
	st := fetchSetup(t, srv, dsn)
	finishedRun(t, st, "t", srv.URL+"/article")
	mustStdout(t, "store", "init")
	mustStdout(t, "store", "fetch")

	stdout, stderr, err := runCmdStdout(t, "store", "extract", "--json")
	if err != nil {
		t.Fatalf("store extract: %v\n%s", err, stderr)
	}
	stats := decode[claims.Stats](t, stdout)
	if stats.Extractor != "qwen-test/claims-v1" || stats.Waiting != 1 || stats.Extracted != 1 || stats.Claims < 1 {
		t.Fatalf("extract = %+v", stats)
	}
	if stats.Sync == nil || stats.Sync.Claims != stats.Claims || stats.Sync.Verified != stats.Claims {
		t.Fatalf("sync = %+v", stats.Sync)
	}
	if stats.Embed == nil || stats.Embed.Embedded != stats.Claims {
		t.Fatalf("embed = %+v", stats.Embed)
	}
	if !strings.Contains(stderr, "Extracting claims with qwen-test") {
		t.Errorf("stderr = %q", stderr)
	}
	files, _ := st.Extractions()
	if len(files) != 1 {
		t.Errorf("%d extraction files", len(files))
	}

	if out := mustStdout(t, "store", "extract"); !strings.Contains(out, "Extracted 0 claim(s) from 0 of 0 waiting text(s) with qwen-test/claims-v1") {
		t.Errorf("second extract: %q", out)
	}
	if _, _, err := runCmdStdout(t, "store", "extract", "--budget", "soon"); err == nil || !strings.Contains(err.Error(), "invalid --budget") {
		t.Errorf("bad --budget = %v", err)
	}

	// One origin: nothing to label, and no model call.
	ls := decode[claims.LinkStats](t, mustStdout(t, "store", "link", "--json"))
	if ls.Checked != stats.Claims || ls.Pairs != 0 || ls.Model != "ollama/qwen-test" {
		t.Errorf("link = %+v", ls)
	}

	// A human link needs indexed claims unless forced.
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var id int64
	if err := conn.QueryRow(context.Background(), `SELECT id FROM claims LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	ref := fmt.Sprintf("C:%d", id)
	if _, _, err := runCmdStdout(t, "store", "link", "set", ref, "C:99", "same"); err == nil || !strings.Contains(err.Error(), "no claim C:99") {
		t.Errorf("unindexed claim = %v", err)
	}
	if _, _, err := runCmdStdout(t, "store", "link", "set", ref, "99", "agrees"); err == nil || !strings.Contains(err.Error(), "unknown relation") {
		t.Errorf("bad relation = %v", err)
	}
	if out := mustStdout(t, "store", "link", "set", ref, "C:99", "Supersedes", "--force", "--note", "newer"); out != "Logged "+ref+" supersedes C:99\n" {
		t.Errorf("forced link: %q", out)
	}
	log, _ := st.ReadLinks(0)
	if len(log.Links) != 1 || log.Links[0].Method != store.LinkHuman || log.Links[0].From != id || log.Links[0].To != 99 || log.Links[0].Note != "newer" {
		t.Errorf("links = %+v", log.Links)
	}

	// The claim is found, looked up and laid out; the link to a claim
	// that isn't indexed isn't shown.
	fr := decode[retrieve.Result](t, mustStdout(t, "find", "article long", "--kind", "claim", "--json"))
	if len(fr.Cards) != 1 || fr.Cards[0].Ref != ref || fr.Cards[0].Kind != retrieve.KindClaim || fr.Cards[0].Quote != "long enough to be the document" {
		t.Fatalf("find claims = %+v", fr.Cards)
	}
	out := mustStdout(t, "find", "article long", "--kind", "claim")
	for _, want := range []string{"1. [" + ref + "] The article is long.", "   single_origin\n", `   quote: "long enough to be the document"`} {
		if !strings.Contains(out, want) {
			t.Errorf("find output lacks %q:\n%s", want, out)
		}
	}
	out = mustStdout(t, "store", "claim", ref)
	for _, want := range []string{"[" + ref + "] The article is long.", "extracted by qwen-test/claims-v1 on ", "[P:", "long enough to be the document"} {
		if !strings.Contains(out, want) {
			t.Errorf("store claim output lacks %q:\n%s", want, out)
		}
	}
	if cr := decode[retrieve.ClaimResult](t, mustStdout(t, "store", "claim", strings.TrimPrefix(ref, "C:"), "--json")); cr.Ref != ref || cr.Passage == nil {
		t.Errorf("store claim --json = %+v", cr)
	}
	if _, _, err := runCmdStdout(t, "store", "claim", "C:99"); err == nil || !strings.Contains(err.Error(), "claim C:99: not in the index") {
		t.Errorf("missing claim = %v", err)
	}
	if out := mustStdout(t, "timeline", ref); !strings.Contains(out, "["+ref+"] The article is long.") || !strings.Contains(out, "1 claim(s), oldest first") {
		t.Errorf("timeline: %q", out)
	}
	if tl := decode[retrieve.TimelineResult](t, mustStdout(t, "timeline", "article long", "--json")); len(tl.Entries) != 1 || !tl.Entries[0].Matched {
		t.Errorf("timeline --json = %+v", tl)
	}

	// A rebuild restores the claims and links from the store.
	rb := decode[index.SyncStats](t, mustStdout(t, "store", "rebuild", "--json"))
	if rb.Claims == nil || rb.Claims.Claims != stats.Claims || rb.Claims.Links != 1 {
		t.Errorf("rebuild claims = %+v", rb.Claims)
	}
}
