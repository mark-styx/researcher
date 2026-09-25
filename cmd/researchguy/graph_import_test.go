package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSources(t *testing.T, entries string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sources.json")
	if err := os.WriteFile(p, []byte(entries), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGraphImportSourcesCmd(t *testing.T) {
	testSetup(t)
	a := writeSources(t, `[
		{"title":"MKUltra","url":"https://en.wikipedia.org/wiki/Project_MKUltra","notes":"a"},
		{"title":"MKUltra again","url":"http://www.en.wikipedia.org/wiki/Project_MKUltra/"},
		{"title":"Only A","url":"https://example.com/a"},
		{"title":"no url"}
	]`)
	b := writeSources(t, `[{"title":"MKUltra","url":"https://en.wikipedia.org/wiki/Project_MKUltra#x"}]`)

	stdout, stderr, err := runCmdStdout(t, "graph", "import-sources", "--book", "book_a", "--file", a, "--title", "Book A", "--path", ".", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	var stats map[string]any
	if err := json.Unmarshal([]byte(stdout), &stats); err != nil {
		t.Fatalf("not JSON: %q", stdout)
	}
	if stats["unique_sources"] != float64(2) || stats["skipped_no_url"] != float64(1) || stats["edges_created"] != float64(2) {
		t.Fatalf("stats = %v", stats)
	}

	stdout, _, err = runCmdStdout(t, "graph", "import-sources", "--book", "book_b", "--file", b)
	if err != nil || !strings.Contains(stdout, "1 unique sources (0 new, 1 existing") {
		t.Fatalf("second book: %q, %v", stdout, err)
	}

	stdout, _, err = runCmdStdout(t, "graph", "list", "--type", "source", "--cited-by-min", "2")
	if err != nil || !strings.Contains(stdout, "MKUltra") || strings.Contains(stdout, "Only A") || !strings.Contains(stdout, "1 node(s) cited by at least 2") {
		t.Fatalf("cited-by list: %q, %v", stdout, err)
	}

	stdout, _, err = runCmdStdout(t, "graph", "show", "https://www.en.wikipedia.org/wiki/Project_MKUltra?utm_source=x")
	if err != nil || !strings.Contains(stdout, "Cited by 2 node(s).") || !strings.Contains(stdout, "Book A --references--> (this)") {
		t.Fatalf("show by url: %q, %v", stdout, err)
	}
}

func TestGraphImportSourcesCmdErrors(t *testing.T) {
	testSetup(t)
	if _, _, err := runCmdStdout(t, "graph", "import-sources", "--book", "x"); err == nil {
		t.Fatal("missing --file should error")
	}
	if _, _, err := runCmdStdout(t, "graph", "import-sources", "--book", "x", "--file", writeSources(t, "{not json")); err == nil {
		t.Fatal("bad JSON should error")
	}
	if _, _, err := runCmdStdout(t, "graph", "show", "https://nowhere.example/x"); err == nil {
		t.Fatal("unknown URL should error")
	}
}
