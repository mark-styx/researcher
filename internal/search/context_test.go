package search

import (
	"context"
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
	body := "#!/bin/sh\necho \"$@\" >> " + argsFile + "\ncat <<'JSONEOF'\n" + string(data) + "\nJSONEOF\n"
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

	got, err := BuildContext(context.Background(), cfg, "topic", ContextOptions{MaxAge: "none", Projects: []string{"research", "the_book"}})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--workspace ws --project research\n") || !strings.Contains(string(args), "--workspace ws --project the_book\n") {
		t.Fatalf("want one grepai call per project, got:\n%s", args)
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

// A minified file is one line, so grepai's one chunk of it can be the
// whole file: three such chunks of ~350 KB made a dive's context 1.1 MB,
// past ARG_MAX for an argv prompt and past most context windows. A file
// included as chunks gets no more room than one included whole.
func TestBuildContextCapsChunkedFile(t *testing.T) {
	cfg, _, book := workspaceFixture(t)
	minified := strings.Repeat("var a=function(){return 1};", 13000)
	path := filepath.Join(book, "research", "web", "page.js")
	if err := os.WriteFile(path, []byte(minified), 0o644); err != nil {
		t.Fatal(err)
	}
	script, _ := fakeGrepai(t, []SearchResult{
		{FilePath: "ws/the_book/research/web/page.js", StartLine: 1, EndLine: 1, Score: 0.7, Content: minified},
	})
	cfg.Grepai.Binary = script

	got, err := BuildContext(context.Background(), cfg, "t", ContextOptions{MaxAge: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 {
		t.Fatalf("count = %d, want the file kept", got.Count)
	}
	if len(got.Context) > MaxWholeFileBytes+200 {
		t.Errorf("context is %d bytes, want the file's chunks cut to about %d", len(got.Context), MaxWholeFileBytes)
	}
	if !strings.Contains(got.Context, "[lines 1-1]\nvar a=function") || !strings.Contains(got.Context, "cut at") {
		t.Errorf("want the chunk's start and a note that it was cut:\n%s", got.Context[len(got.Context)-300:])
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

	got, err := BuildContext(context.Background(), cfg, "t", ContextOptions{})
	if err != nil || got.Count != 0 || got.Sources == nil {
		t.Fatalf("stale hit should be filtered with a non-nil empty list: %+v, %v", got, err)
	}
}

func TestBuildContextErrors(t *testing.T) {
	cfg := &config.Config{ResearchDir: t.TempDir(), Grepai: config.GrepaiConfig{Binary: "nonexistent-grepai-xyz"}}
	if _, err := BuildContext(context.Background(), cfg, "t", ContextOptions{MaxAge: "5x"}); err == nil || !strings.Contains(err.Error(), "invalid max_age") {
		t.Fatalf("err = %v", err)
	}
	if _, err := BuildContext(context.Background(), cfg, "t", ContextOptions{}); err == nil {
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

// bookworm writes both per-topic files and a combined file that repeats
// them, so grepai returns the same text under different paths.
func TestBuildContextSkipsDuplicateText(t *testing.T) {
	cfg, _, book := workspaceFixture(t)
	web := filepath.Join(book, "research", "web")
	big := strings.Repeat("filler ", MaxWholeFileBytes/7+1)
	files := map[string]string{
		"combined.md":  big + "same text" + big,
		"combined2.md": big + "chunk only" + big,
		"copy.md":      big + "chunk only" + big,
		"topic.md":     "same text\n\nother text",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(web, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hit := func(name string, score float64, text string) SearchResult {
		return SearchResult{FilePath: "ws/the_book/research/web/" + name, Score: score, Content: "File: research/web/" + name + "\n\n" + text}
	}
	script, _ := fakeGrepai(t, []SearchResult{
		hit("topic.md", 0.6, "other text"),
		hit("combined.md", 0.9, "same text"),
		hit("topic.md", 0.8, "same text"),
		hit("combined2.md", 0.5, "chunk only"),
		hit("copy.md", 0.4, "chunk only"),
	})
	cfg.Grepai.Binary = script

	got, err := BuildContext(context.Background(), cfg, "t", ContextOptions{MaxAge: "none"})
	if err != nil {
		t.Fatal(err)
	}
	// combined.md's only chunk is inside topic.md (included whole), and
	// copy.md's only chunk repeats combined2.md's, so both drop out.
	var names []string
	for _, s := range got.Sources {
		names = append(names, filepath.Base(s.FilePath))
	}
	if strings.Join(names, ",") != "topic.md,combined2.md" {
		t.Fatalf("sources = %v", names)
	}
	if strings.Count(got.Context, "same text") != 1 || strings.Count(got.Context, "chunk only") != 1 {
		t.Fatalf("duplicate text in context:\n%s", got.Context)
	}
	if got.Count != 2 {
		t.Fatalf("count = %d", got.Count)
	}
}
