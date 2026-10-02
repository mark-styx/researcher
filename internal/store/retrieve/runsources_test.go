package retrieve

import (
	"context"
	"strings"
	"testing"

	"github.com/marklubin/researchguy/internal/store"
)

func TestRunSources_FetchedFirstThenSeen(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	run := fetchRun(t, f.st, catDoc)
	run.Append(
		store.Capture{Call: store.Call{Tool: "web_search", Action: "search", Query: "cats", Results: []store.CaptureResult{
			{Rank: 1, Title: "Blocked | page", URL: "https://blocked.example/x"},
			{Rank: 2, Title: "Unfetched", URL: "https://later.example/y"},
			{Rank: 3, Title: "Cats", URL: catDoc.url},
		}}},
		store.Capture{Call: store.Call{Tool: "web_fetch", Action: "open", URL: catDoc.url}, Content: "cats"},
	)
	log, err := store.OpenFetchLog(run.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.Append(store.FetchRecord{URL: "https://blocked.example/x", Reason: store.ReasonResult, Rank: 1, Via: "direct",
		AttemptedAt: t2026, Attempts: 1, HTTPStatus: 403, Error: "HTTP 403"}); err != nil {
		t.Fatal(err)
	}
	log.Close()
	if _, err := f.ix.IngestRun(ctx, run.Dir(), false); err != nil {
		t.Fatal(err)
	}

	res, err := f.r.RunSources(ctx, run.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 3 || len(res.Sources) != 3 {
		t.Fatalf("sources = %+v", res)
	}
	cat, blocked, later := res.Sources[0], res.Sources[1], res.Sources[2]
	if cat.URL != catDoc.url || cat.DocumentID != f.byURL[catDoc.url] || cat.ContentKind != store.KindFull || cat.Passages != 1 {
		t.Errorf("fetched source = %+v", cat)
	}
	if cat.Published == nil || cat.Published.Date != "2020-01-15" || cat.Collected == nil || strings.Join(cat.Seen, ",") != "E1,E2" {
		t.Errorf("fetched source dates/seen = %+v", cat)
	}
	if blocked.URL != "https://blocked.example/x" || !blocked.Tried || blocked.FetchError != "HTTP 403" || blocked.DocumentID != 0 {
		t.Errorf("blocked source = %+v", blocked)
	}
	if later.Tried || strings.Join(later.Seen, ",") != "E1" {
		t.Errorf("unfetched source = %+v", later)
	}

	table := res.Table()
	for _, want := range []string{
		"| [" + cat.Ref + "] | zoo.example | Title of " + catDoc.url + " | 2020-01-15 | 2025-03-01 | full, 1 passage | E1, E2 |",
		"fetch failed: HTTP 403",
		"| not fetched | E1 |",
	} {
		if !strings.Contains(table, want) {
			t.Errorf("table missing %q:\n%s", want, table)
		}
	}

	res, err = f.r.RunSources(ctx, run.ID(), 1)
	if err != nil || res.Total != 3 || len(res.Sources) != 1 || !strings.Contains(res.Table(), "2 more sources not listed.") {
		t.Errorf("limited = %+v, %v", res, err)
	}
}

func TestRunSources_EmptyRun(t *testing.T) {
	f := newFixture(t)
	res, err := f.r.RunSources(context.Background(), "no-such-run", 10)
	if err != nil || res.Total != 0 || res.Table() != "(no sources recorded for this run)\n" {
		t.Errorf("empty = %+v, %v", res, err)
	}
}

func TestCell(t *testing.T) {
	if got := cell("a | b\n c", 40); got != "a / b c" {
		t.Errorf("cell = %q", got)
	}
	if got := cell(strings.Repeat("x", 50), 10); got != "xxxxxxx..." {
		t.Errorf("long cell = %q", got)
	}
}
