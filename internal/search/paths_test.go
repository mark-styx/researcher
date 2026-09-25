package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marklubin/researchguy/internal/config"
)

// workspaceFixture builds a research dir, a book project, and a grepai
// workspace registry that points at both.
func workspaceFixture(t *testing.T) (*config.Config, string, string) {
	t.Helper()
	base := t.TempDir()
	research := filepath.Join(base, "research")
	book := filepath.Join(base, "books", "the_book")
	for _, d := range []string{filepath.Join(research, "cat"), filepath.Join(book, "research", "web")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	registry := filepath.Join(base, "workspace.yaml")
	yaml := "version: 1\nworkspaces:\n  ws:\n    name: ws\n    projects:\n      - name: research\n        path: " + research +
		"\n      - name: the_book\n        path: " + book +
		"\n  other:\n    projects:\n      - name: the_book\n        path: /elsewhere\n"
	if err := os.WriteFile(registry, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		ResearchDir: research,
		Grepai:      config.GrepaiConfig{Workspace: "ws", Project: "research", WorkspaceFile: registry},
	}
	return cfg, research, book
}

func TestProjectRoots(t *testing.T) {
	cfg, research, book := workspaceFixture(t)
	roots, err := ProjectRoots(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if roots["research"] != research || roots["the_book"] != book || len(roots) != 2 {
		t.Fatalf("roots = %v", roots)
	}

	cfg.Grepai.WorkspaceFile = filepath.Join(t.TempDir(), "missing.yaml")
	if _, err := ProjectRoots(cfg); err == nil {
		t.Fatal("missing registry should error")
	}
	cfg.Grepai.Workspace = ""
	if roots, err := ProjectRoots(cfg); err != nil || len(roots) != 0 {
		t.Fatalf("no workspace: %v, %v", roots, err)
	}
}

func TestResolverResolve(t *testing.T) {
	cfg, research, book := workspaceFixture(t)
	r := NewResolver(cfg)
	cases := []struct {
		in, project, abs string
	}{
		{"ws/research/cat/a.md", "research", filepath.Join(research, "cat", "a.md")},
		{"ws/the_book/research/web/x.md", "the_book", filepath.Join(book, "research", "web", "x.md")},
		{"ws/unknown/x.md", "unknown", ""},
		{"cat/a.md", "", filepath.Join(research, "cat", "a.md")},
		{"/abs/file.md", "", "/abs/file.md"},
	}
	for _, tc := range cases {
		p, abs := r.Resolve(tc.in)
		if p != tc.project || abs != tc.abs {
			t.Errorf("Resolve(%q) = %q, %q; want %q, %q", tc.in, p, abs, tc.project, tc.abs)
		}
	}
}

func TestResolverWithoutRegistryFallsBackForResearchProject(t *testing.T) {
	cfg, research, _ := workspaceFixture(t)
	cfg.Grepai.WorkspaceFile = filepath.Join(t.TempDir(), "missing.yaml")
	p, abs := NewResolver(cfg).Resolve("ws/research/cat/a.md")
	if p != "research" || abs != filepath.Join(research, "cat", "a.md") {
		t.Fatalf("got %q, %q", p, abs)
	}
}

// Before path resolution existed, FilterFresh joined workspace hit paths onto
// research_dir, so every workspace hit failed os.Stat and was dropped.
func TestFilterFreshKeepsWorkspaceHits(t *testing.T) {
	cfg, research, book := workspaceFixture(t)
	for _, p := range []string{filepath.Join(research, "cat", "a.md"), filepath.Join(book, "research", "web", "x.md")} {
		if err := os.WriteFile(p, []byte("content"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	results := Annotate(cfg, []SearchResult{
		{FilePath: "ws/research/cat/a.md"},
		{FilePath: "ws/the_book/research/web/x.md"},
	})
	fresh := FilterFresh(results, research, 24*time.Hour)
	if len(fresh) != 2 {
		t.Fatalf("got %d fresh results, want 2", len(fresh))
	}
	if fresh[1].Project != "the_book" || !strings.HasPrefix(fresh[1].Path, book) {
		t.Fatalf("annotation lost: %+v", fresh[1])
	}
	contents := ReadContents(fresh, research)
	if len(contents) != 2 {
		t.Fatalf("read %d files, want 2", len(contents))
	}
}

func TestFilterFreshZeroMaxAgeKeepsEverything(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.md")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-400 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	in := []SearchResult{{FilePath: "old.md"}}
	if got := FilterFresh(in, dir, 0); len(got) != 1 {
		t.Fatalf("maxAge 0 dropped results: %v", got)
	}
	if got := FilterFresh(in, dir, 24*time.Hour); len(got) != 0 {
		t.Fatalf("stale file kept: %v", got)
	}
}

func TestParseMaxAgeNone(t *testing.T) {
	for _, s := range []string{"none", "NONE", " off ", "all"} {
		d, err := ParseMaxAge(s)
		if err != nil || d != 0 {
			t.Errorf("ParseMaxAge(%q) = %v, %v", s, d, err)
		}
	}
}

func TestWorkspaceArgs(t *testing.T) {
	cfg := &config.Config{Grepai: config.GrepaiConfig{Workspace: "ws", Project: "research"}}
	cases := []struct {
		name     string
		projects []string
		cfgList  []string
		want     string
	}{
		{"config single", nil, nil, "--workspace ws --project research"},
		{"config many", nil, []string{"a", "b"}, "--workspace ws --project a --project b"},
		{"override", []string{"x"}, []string{"a", "b"}, "--workspace ws --project x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := *cfg
			c.Grepai.Projects = tc.cfgList
			if got := strings.Join(workspaceArgs(&c, tc.projects), " "); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	if got := workspaceArgs(&config.Config{}, []string{"x"}); got != nil {
		t.Fatalf("no workspace should give nil, got %v", got)
	}
	noProject := &config.Config{Grepai: config.GrepaiConfig{Workspace: "ws"}}
	if got := strings.Join(workspaceArgs(noProject, nil), " "); got != "--workspace ws" {
		t.Fatalf("got %q", got)
	}
}

// grepai applies several --project flags as a post-filter on the
// workspace-wide top-k, which can return nothing even when each project has
// strong hits. QueryJSONProjects therefore queries one project per call.
func TestQueryJSONProjectsQueriesEachProject(t *testing.T) {
	cfg, _, _ := workspaceFixture(t)
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	// Fake grepai: one hit per call, scored by project, and an empty list if
	// more than one --project is passed (the post-filter failure mode).
	script := filepath.Join(dir, "fake-grepai")
	body := `#!/bin/sh
echo "$@" >> ` + calls + `
n=0; proj=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--project" ]; then n=$((n+1)); proj="$2"; shift; fi
  shift
done
if [ "$n" -ne 1 ]; then echo '[]'; exit 0; fi
case "$proj" in
  research) score=0.4 ;;
  the_book) score=0.9 ;;
  *) score=0.1 ;;
esac
echo "[{\"file_path\":\"ws/$proj/a.md\",\"score\":$score,\"content\":\"x\"},{\"file_path\":\"ws/$proj/b.md\",\"score\":0.05,\"content\":\"y\"}]"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Grepai.Binary = script

	got, err := QueryJSONProjects(cfg, "q", 3, []string{"research", "the_book", "other"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Project != "the_book" || got[1].Project != "research" || got[2].Project != "other" {
		t.Fatalf("merged results = %+v", got)
	}
	data, _ := os.ReadFile(calls)
	if n := strings.Count(string(data), "\n"); n != 3 {
		t.Fatalf("want 3 grepai calls, got %d:\n%s", n, data)
	}

	// A single project stays a single call.
	os.Remove(calls)
	if got, err := QueryJSONProjects(cfg, "q", 5, []string{"research"}); err != nil || len(got) != 2 {
		t.Fatalf("single project: %v, %v", got, err)
	}
}

func TestQueryJSONProjectsPartialFailure(t *testing.T) {
	cfg, _, _ := workspaceFixture(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-grepai")
	body := "#!/bin/sh\ncase \"$*\" in *broken*) echo boom >&2; exit 1;; esac\necho '[{\"file_path\":\"ws/research/a.md\",\"score\":0.5,\"content\":\"x\"}]'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Grepai.Binary = script
	got, err := QueryJSONProjects(cfg, "q", 5, []string{"research", "broken"})
	if err != nil || len(got) != 1 {
		t.Fatalf("one failing project should not sink the search: %v, %v", got, err)
	}
	if _, err := QueryJSONProjects(cfg, "q", 5, []string{"broken", "broken2"}); err == nil {
		t.Fatal("all projects failing should error")
	}
}
