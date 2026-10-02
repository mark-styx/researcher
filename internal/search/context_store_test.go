package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/embed"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
	"github.com/marklubin/researchguy/internal/store/index/indextest"
	"github.com/marklubin/researchguy/internal/store/retrieve"
)

const reportText = "# Wells\n\nOur earlier report: nitrate in wells near feedlots is rising, per the county survey."

// storeFixture adds a store index to cfg holding one fetched document,
// collected a year ago, and one report, and returns the report's path.
func storeFixture(t *testing.T, cfg *config.Config) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []string }
		json.NewDecoder(r.Body).Decode(&req)
		out := make([][]float32, len(req.Input))
		for i := range out {
			out[i] = make([]float32, index.Dims)
			out[i][0] = 1
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": out})
	}))
	t.Cleanup(srv.Close)
	cfg.Store.Dir = filepath.Join(t.TempDir(), "store")
	cfg.Store.DSN = indextest.DSN(t)
	cfg.Store.Embed.Host = srv.URL

	st, err := store.Open(cfg.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := st.StartRun(store.RunRecord{Kind: "dive", Topic: "wells"})
	text := "County survey: nitrate in wells near feedlots rose 40 percent between 2019 and 2024."
	sha, _ := st.PutText(text)
	log, err := store.OpenFetchLog(run.Dir())
	if err != nil {
		t.Fatal(err)
	}
	log.Append(store.FetchRecord{URL: "https://county.example/survey", Reason: store.ReasonCited, Via: "direct",
		AttemptedAt: time.Now().Add(-365 * 24 * time.Hour), Attempts: 1, HTTPStatus: 200, TextSHA256: sha, TextChars: len(text),
		ContentKind: store.KindFull, Title: "Well survey", Published: &store.Published{Date: "2024-11", Precision: "month", From: "jsonld"}})
	log.Close()
	report := filepath.Join(cfg.ResearchDir, "cat", "wells.md")
	if err := os.WriteFile(report, []byte(reportText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(store.Finish{Status: store.StatusSucceeded, ReportPath: report}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ix, err := index.Open(ctx, cfg.Store.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	if _, err := ix.Sync(ctx, st); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Embed(ctx, st, embed.New(cfg)); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestBuildContext_StoreEvidence(t *testing.T) {
	cfg, _, _ := workspaceFixture(t)
	report := storeFixture(t, cfg)
	cfg.Ask.MaxAge = "90d"
	script, _ := fakeGrepai(t, []SearchResult{{FilePath: "ws/research/cat/wells.md", Score: 0.9, Content: reportText}})
	cfg.Grepai.Binary = script
	ctx := context.Background()

	// The year-old document stays: ask.max_age limits report files only.
	got, err := BuildContext(ctx, cfg, "nitrate wells feedlots", ContextOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 1 || len(got.Evidence) != 1 || got.Count != 2 || len(got.Notes) != 0 {
		t.Fatalf("context = sources %+v evidence %+v notes %v", got.Sources, got.Evidence, got.Notes)
	}
	// The report's own passage is dropped: the report file is included whole.
	ev := got.Evidence[0]
	if ev.URL != "https://county.example/survey" || !strings.Contains(got.Context, "--- Evidence ["+ev.Ref+"]: Well survey (primary evidence; published 2024-11") ||
		!strings.Contains(got.Context, "--- Source: research/cat/wells.md ---") {
		t.Errorf("context text:\n%s", got.Context)
	}

	// An explicit max age limits store evidence by collection date.
	got, _ = BuildContext(ctx, cfg, "nitrate wells feedlots", ContextOptions{MaxAge: "30d"})
	for _, c := range got.Evidence {
		if c.Kind == "passage" {
			t.Errorf("30d kept a year-old document: %+v", c)
		}
	}

	// Without the report file, the report's passage comes from the store,
	// labeled synthesis; with grepai gone, store evidence still returns.
	os.Remove(report)
	cfg.Grepai.Binary = "nonexistent-grepai-xyz"
	got, err = BuildContext(ctx, cfg, "nitrate wells feedlots", ContextOptions{MaxAge: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Evidence) != 2 || !strings.Contains(got.Context, "researchguy synthesis, not primary evidence") ||
		len(got.Notes) != 1 || !strings.Contains(got.Notes[0], "report search failed") {
		t.Errorf("without grepai = evidence %d notes %v\n%s", len(got.Evidence), got.Notes, got.Context)
	}
}

func TestBuildContext_StoreDown(t *testing.T) {
	cfg, research, _ := workspaceFixture(t)
	os.WriteFile(filepath.Join(research, "cat", "r.md"), []byte("body"), 0o644)
	script, _ := fakeGrepai(t, []SearchResult{{FilePath: "ws/research/cat/r.md", Content: "body"}})
	cfg.Grepai.Binary = script
	cfg.Store.DSN = "postgres://localhost:1/none?connect_timeout=2"
	cfg.Store.Dir = t.TempDir()
	got, err := BuildContext(context.Background(), cfg, "t", ContextOptions{MaxAge: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 || len(got.Notes) != 1 || !strings.Contains(got.Notes[0], "store index unavailable") {
		t.Errorf("store down = %+v", got)
	}
}

func TestFormatEvidence_Claim(t *testing.T) {
	c := retrieve.Card{Ref: "C:5", Kind: retrieve.KindClaim, Title: "Nitrate survey", URL: "https://county.example/survey", Age: "published 2022-08-01",
		Text: "Nitrate levels rose.", Quote: "nitrate levels rose", QuoteVerified: true, Flags: []string{retrieve.FlagReinforced, retrieve.FlagContested},
		Cluster: &retrieve.Cluster{Origins: 2, SupportDates: "2021-2022", Related: []retrieve.Related{
			{Ref: "C:9", Relation: retrieve.RelContradicts, Method: store.LinkModel, Domain: "farm.example", Dated: "2023-01-01", Text: "Levels held steady."},
			{Ref: "C:7", Relation: retrieve.RelSame, Method: store.LinkRule, Domain: "news.example", Dated: "2021-05-01", Text: "Nitrate went up."},
		}}}
	got := FormatEvidence([]retrieve.Card{c})
	want := `--- Evidence [C:5]: claim from Nitrate survey (primary evidence; reinforced, contested; published 2022-08-01) ---
https://county.example/survey
Nitrate levels rose.
Quote: "nitrate levels rose"
Said by 2 independent origins (2021-2022)
contradicts [C:9] (farm.example, 2023-01-01; model-labeled, an inference): Levels held steady.
same [C:7] (news.example, 2021-05-01; rule): Nitrate went up.

`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	c.QuoteVerified, c.Flags, c.Cluster = false, nil, nil
	if got := FormatEvidence([]retrieve.Card{c}); strings.Contains(got, "Quote:") || !strings.Contains(got, "no cluster flags") {
		t.Errorf("unverified, unlinked claim:\n%s", got)
	}
}
