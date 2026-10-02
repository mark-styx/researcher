package research

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/critique"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
	"github.com/marklubin/researchguy/internal/store/index/indextest"
)

// aggregatingProvider stands in for the hybrid backend: it captures a
// search, calls BeforeAggregate where the hybrid does, before its
// aggregator, and writes the report from what that gave it.
type aggregatingProvider struct {
	capture llm.EvidenceRecord
	report  func(in llm.AggregateInput) string

	hooked  bool
	in      llm.AggregateInput
	hookErr error
	cited   []critique.Evidence
}

func (a *aggregatingProvider) Name() string { return "hybrid" }

func (a *aggregatingProvider) Complete(ctx context.Context, req llm.Request) (string, error) {
	req.Capture(a.capture)
	if req.BeforeAggregate != nil {
		a.hooked = true
		a.in, a.hookErr = req.BeforeAggregate(ctx)
	}
	draft := a.report(a.in)
	if a.in.Cited != nil {
		a.cited = a.in.Cited(ctx, draft)
	}
	return draft, nil
}

func TestRun_AggregatorGetsRunSourcesAndCitationsAreChecked(t *testing.T) {
	srv, _ := sourceSite(t)
	cfg := fetchConfig(t, srv)
	cfg.Store.DSN = indextest.DSN(t)
	cfg.Hybrid.AggregatorTools = true
	ix, err := index.Open(context.Background(), cfg.Store.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()

	var pid, sid int64
	p := &aggregatingProvider{
		capture: llm.EvidenceRecord{Label: "web_search", Content: "results", Call: store.Call{Tool: "web_search", Action: "search", Query: "q",
			Results: []store.CaptureResult{{Rank: 1, URL: srv.URL + "/article"}, {Rank: 2, URL: srv.URL + "/missing"}}}},
		report: func(llm.AggregateInput) string {
			// The run's own fetch is indexed by now.
			err := ix.Pool().QueryRow(context.Background(), `SELECT p.id, s.id FROM passages p JOIN documents d ON d.id = p.document_id
				JOIN sources s ON s.id = d.source_id WHERE s.url = $1 ORDER BY p.ord LIMIT 1`, srv.URL+"/article").Scan(&pid, &sid)
			if err != nil {
				t.Errorf("article passage not indexed before aggregation: %v", err)
			}
			P, S := strconv.FormatInt(pid, 10), strconv.FormatInt(sid, 10)
			return "Intro \"A paragraph of the fetched article, long enough\" [P:" + P + "]. Also [S:" + S + "] and [E1].\n" +
				"Wrong: \"this sentence is not in the article\" [P:" + P + "]. Missing [P:42].\n"
		},
	}
	out := filepath.Join(t.TempDir(), "report.md")
	res, err := NewRunner(cfg, p).Run(context.Background(), Task{Type: TypeDive, Topic: "t", NoResearch: true, Quiet: true, OutPath: out})
	if err != nil {
		t.Fatal(err)
	}

	if !p.hooked || p.hookErr != nil || p.in.RunID != res.RunID {
		t.Fatalf("hook = %v, %v, run %q", p.hooked, p.hookErr, p.in.RunID)
	}
	S := "[S:" + strconv.FormatInt(sid, 10) + "]"
	for _, want := range []string{S, "full, ", "fetch failed: HTTP 404", "E1"} {
		if !strings.Contains(p.in.Sources, want) {
			t.Errorf("sources table missing %q:\n%s", want, p.in.Sources)
		}
	}
	if len(p.in.MCP) != 1 || strings.Join(p.in.MCP[0].Args, " ") != "mcp --profile read" || p.in.MCP[0].Env["RESEARCHGUY_CONFIG_DIR"] != config.Dir() {
		t.Errorf("MCP = %+v", p.in.MCP)
	}
	if len(p.cited) != 1 || !strings.Contains(p.cited[0].Content, "A paragraph of the fetched article") {
		t.Errorf("critic evidence = %+v", p.cited)
	}

	cs, ok, err := store.ReadCitations(res.RunDir)
	if err != nil || !ok || len(cs) != 5 {
		t.Fatalf("citations = %+v, %v, %v", cs, ok, err)
	}
	statuses := []string{}
	for _, c := range cs {
		statuses = append(statuses, c.Marker[:1]+":"+strconv.FormatBool(c.Resolved != nil && *c.Resolved)+":"+c.QuoteStatus)
	}
	if got := strings.Join(statuses, " "); got != "P:true:found S:true: E:true: P:true:not_found P:false:" {
		t.Errorf("citations = %s", got)
	}

	report, _ := os.ReadFile(out)
	for _, want := range []string{"\n## Citation Check\n", "[P:42] doesn't resolve: no such passage in the store.", `"this sentence is not in the article"`} {
		if !strings.Contains(string(report), want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
	// The store's copy of the report has the notes too.
	rec, _ := store.ReadRecord(res.RunDir)
	st, _ := store.Open(cfg.Store.Dir)
	if text, err := st.ReadText(rec.ReportSHA256); err != nil || !strings.Contains(text, "## Citation Check") {
		t.Errorf("stored report = %v, %q", err, text)
	}

	// Switched off, the aggregator gets the table but no tools.
	cfg.Hybrid.AggregatorTools = false
	p.hooked = false
	if _, err := NewRunner(cfg, p).Run(context.Background(), Task{Type: TypeDive, Topic: "t", NoResearch: true, Quiet: true,
		OutPath: filepath.Join(t.TempDir(), "r2.md")}); err != nil {
		t.Fatal(err)
	}
	if !p.hooked || p.in.MCP != nil || p.in.Sources == "" {
		t.Errorf("aggregator_tools off: hooked %v, input %+v", p.hooked, p.in)
	}
}

func TestRun_CitationCheckWithoutIndex(t *testing.T) {
	cfg := storeConfig(t)
	p := &evidenceProvider{
		mockProvider: mockProvider{response: "Quoted \"words the page never said at all\" [E1], and [P:5]."},
		evidence:     []llm.EvidenceRecord{{Label: "web_fetch", Content: "the page text", Call: store.Call{Tool: "web_fetch", Action: "open", URL: "https://x.example"}}},
	}
	out := filepath.Join(t.TempDir(), "report.md")
	res, err := NewRunner(cfg, p).Run(context.Background(), Task{Type: TypeDive, Topic: "t", NoResearch: true, Quiet: true, OutPath: out})
	if err != nil {
		t.Fatal(err)
	}
	if p.calls[0].BeforeAggregate != nil {
		t.Error("BeforeAggregate set without an index")
	}
	cs, _, err := store.ReadCitations(res.RunDir)
	if err != nil || len(cs) != 2 || cs[0].QuoteStatus != store.QuoteNotFound || cs[1].Resolved != nil {
		t.Fatalf("citations = %+v, %v", cs, err)
	}
	report, _ := os.ReadFile(out)
	if !strings.Contains(string(report), "1 not checked") || !strings.Contains(string(report), "[E1]: the quote isn't in the cited text.") {
		t.Errorf("report = %s", report)
	}
}

func TestRun_CitationNotesGoOnTheirWatchUpdate(t *testing.T) {
	cfg := storeConfig(t)
	out := filepath.Join(t.TempDir(), "watch.md")
	p := &mockProvider{responses: []string{"First update cites [E9].", "Second update cites nothing."}}
	runner := NewRunner(cfg, p)
	for range 2 {
		if _, err := runner.Run(context.Background(), Task{Type: TypeWatch, Topic: "t", Quiet: true, OutPath: out}); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(out)
	text := string(b)
	notes := strings.Index(text, "## Citation Check")
	second := strings.Index(text, "Second update")
	if strings.Count(text, "## Citation Check") != 1 || notes < 0 || second < notes || !strings.Contains(text, "[E9] doesn't resolve") {
		t.Errorf("watch file:\n%s", text)
	}
}
