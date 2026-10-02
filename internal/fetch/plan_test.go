package fetch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marklubin/researchguy/internal/store"
)

func TestPlan_OrderDedupAndTopN(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "report.md")
	os.WriteFile(report, []byte("# T\n\n*Generated | Run: r1*\n\nSee [a](https://cited.example/paper) and https://results.example/3?utm_source=x."), 0o644)
	os.WriteFile(filepath.Join(dir, "workers.jsonl"), []byte(`{"worker":1,"content":"draft cites https://draft.example/d"}`+"\n"), 0o644)
	captures := []store.Capture{
		{Seq: 1, Call: store.Call{Tool: "web_search", Action: "search", Query: "q", Results: []store.CaptureResult{
			{Rank: 1, URL: "https://results.example/1"},
			{Rank: 2, URL: "https://results.example/2"},
			{Rank: 3, URL: "https://www.results.example/3"},
			{Rank: 4, URL: "https://results.example/4"},
			{Rank: 2, URL: "https://results.example/img.png"},
		}}},
		{Seq: 2, Call: store.Call{Tool: "web_search", Action: "open", URL: "https://opened.example/o",
			Results: []store.CaptureResult{{Opened: true, URL: "https://opened.example/o"}}}},
		{Seq: 3, Call: store.Call{Tool: "web_fetch", Action: "fetch", URL: "https://results.example/2"}},
		{Seq: 4, Call: store.Call{Tool: "web_search", Action: "search", Results: []store.CaptureResult{
			{Rank: 1, URL: "https://doi.org/10.1234/abcd"},
		}}},
	}
	got := Plan(dir, store.RunRecord{ID: "r1", ReportPath: report}, captures, 3)
	type tr struct {
		url, reason string
		rank        int
	}
	want := []tr{
		{"https://draft.example/d", store.ReasonCited, 0},
		{"https://cited.example/paper", store.ReasonCited, 0},
		{"https://results.example/3?utm_source=x", store.ReasonCited, 0},
		{"https://results.example/2", store.ReasonOpened, 0},
		{"https://opened.example/o", store.ReasonOpened, 0},
		{"https://results.example/1", store.ReasonResult, 1},
		{"https://doi.org/10.1234/abcd", store.ReasonResult, 1},
	}
	if len(got) != len(want) {
		t.Fatalf("Plan = %+v", got)
	}
	for i, w := range want {
		if got[i].URL != w.url || got[i].Reason != w.reason || got[i].Rank != w.rank {
			t.Errorf("target %d = %+v, want %+v", i, got[i], w)
		}
	}
	if got[6].DOI != "10.1234/abcd" {
		t.Errorf("DOI = %q", got[6].DOI)
	}
}

func TestRunSection_WatchFile(t *testing.T) {
	watch := "# Topic — Watch Updates\n\n---\n\n## Update: 2026-09-01\n\n*Backend: x | Run: old*\n\nold https://old.example\n" +
		"\n\n---\n\n## Update: 2026-10-01\n\n*Backend: x | Run: new*\n\nnew https://new.example\n" +
		"\n\n---\n\n## Update: 2026-10-02\n\n*Backend: x | Run: newer*\n\nnewer https://newer.example\n"
	sec := runSection([]byte(watch), "new")
	if !strings.Contains(sec, "https://new.example") || strings.Contains(sec, "old.example") || strings.Contains(sec, "newer.example") {
		t.Errorf("section = %q", sec)
	}
	if !strings.HasPrefix(sec, "\n## Update: 2026-10-01") {
		t.Errorf("section doesn't start at its update header: %q", sec[:30])
	}
	if got := runSection([]byte("no tag https://x.example"), "zzz"); got != "no tag https://x.example" {
		t.Errorf("untagged report = %q", got)
	}
}

func TestCitedURLs_NoFiles(t *testing.T) {
	if got := CitedURLs(t.TempDir(), store.RunRecord{ReportPath: "/does/not/exist.md"}); len(got) != 0 {
		t.Errorf("CitedURLs = %v", got)
	}
}
