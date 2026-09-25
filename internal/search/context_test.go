package search

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/marklubin/researchguy/internal/config"
)

// fakeGrepai writes a script that records its args and prints results as JSON.
func fakeGrepai(t *testing.T, results []SearchResult) (script, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	data, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	argsFile = filepath.Join(dir, "args")
	script = filepath.Join(dir, "fake-grepai")
	body := "#!/bin/sh\necho \"$@\" > " + argsFile + "\ncat <<'JSONEOF'\n" + string(data) + "\nJSONEOF\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, argsFile
}

func TestBuildContextAcrossProjects(t *testing.T) {
	cfg, research, book := workspaceFixture(t)
	small := filepath.Join(research, "cat", "report.md")
	big := filepath.Join(book, "research", "web", "001-researcher-1.md")
	if err := os.WriteFile(small, []byte("small report body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, []byte(strings.Repeat("x", MaxWholeFileBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	// Book research is old; "none" must keep it.
	past := time.Now().Add(-300 * 24 * time.Hour)
	if err := os.Chtimes(big, past, past); err != nil {
		t.Fatal(err)
	}

	script, argsFile := fakeGrepai(t, []SearchResult{
		{FilePath: "ws/research/cat/report.md", StartLine: 1, EndLine: 1, Score: 0.9, Content: "small report body"},
		{FilePath: "ws/the_book/research/web/001-researcher-1.md", StartLine: 40, EndLine: 45, Score: 0.5, Content: "second chunk"},
		{FilePath: "ws/the_book/research/web/001-researcher-1.md", StartLine: 10, EndLine: 12, Score: 0.7, Content: "first chunk"},
	})
	cfg.Grepai.Binary = script

	got, err := BuildContext(cfg, "topic", ContextOptions{MaxAge: "none", Projects: []string{"research", "the_book"}})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--project research --project the_book") {
		t.Fatalf("projects not passed: %s", args)
	}
	if got.Count != 2 || got.Sources[1].Project != "the_book" || got.Sources[1].Path != big {
		t.Fatalf("sources = %+v", got.Sources)
	}
	if !strings.Contains(got.Context, "--- Source: research/cat/report.md ---\nsmall report body") {
		t.Fatalf("small file not included whole:\n%s", got.Context)
	}
	if strings.Contains(got.Context, strings.Repeat("x", 100)) {
		t.Fatal("large file included whole")
	}
	first := strings.Index(got.Context, "[lines 10-12]\nfirst chunk")
	second := strings.Index(got.Context, "[lines 40-45]\nsecond chunk")
	if first < 0 || second < first {
		t.Fatalf("chunks missing or out of order:\n%s", got.Context)
	}
}

func TestBuildContextFreshnessDefault(t *testing.T) {
	cfg, research, _ := workspaceFixture(t)
	old := filepath.Join(research, "cat", "old.md")
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-200 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	script, _ := fakeGrepai(t, []SearchResult{{FilePath: "ws/research/cat/old.md", Content: "old"}})
	cfg.Grepai.Binary = script
	cfg.Ask.MaxAge = "90d"

	got, err := BuildContext(cfg, "t", ContextOptions{})
	if err != nil || got.Count != 0 || got.Sources == nil {
		t.Fatalf("stale hit should be filtered with a non-nil empty list: %+v, %v", got, err)
	}
}

func TestBuildContextErrors(t *testing.T) {
	cfg := &config.Config{ResearchDir: t.TempDir(), Grepai: config.GrepaiConfig{Binary: "nonexistent-grepai-xyz"}}
	if _, err := BuildContext(cfg, "t", ContextOptions{MaxAge: "5x"}); err == nil || !strings.Contains(err.Error(), "invalid max_age") {
		t.Fatalf("err = %v", err)
	}
	if _, err := BuildContext(cfg, "t", ContextOptions{}); err == nil {
		t.Fatal("missing grepai should error")
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("short", 10); got != "short" {
		t.Fatalf("got %q", got)
	}
	got := truncateRunes(strings.Repeat("é", 10), 5)
	if !utf8.ValidString(got) || got != "éé..." {
		t.Fatalf("got %q", got)
	}
}
