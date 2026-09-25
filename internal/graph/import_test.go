package graph

import (
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/marklubin/researchguy/internal/config"
)

var bookA = []SourceEntry{
	{Title: "Project MKUltra", URL: "https://en.wikipedia.org/wiki/Project_MKUltra", Notes: "overview", Type: "web"},
	{Title: "Project MKUltra (dup)", URL: "http://en.wikipedia.org/wiki/Project_MKUltra#Background", Notes: "background section"},
	{Title: "Church Committee", URL: "https://www.senate.gov/church/", Author: "US Senate", Date: "1976"},
	{Title: "No URL book", URL: ""},
	{Title: "Bad", URL: "ftp://example.com/file"},
}

var bookB = []SourceEntry{
	{Title: "MKUltra - Wikipedia", URL: "https://en.wikipedia.org/wiki/Project_MKUltra", Notes: "used for chapter 2"},
	{Title: "Mockingbird", URL: "https://example.org/mockingbird?utm_source=news"},
}

func TestImportSources_CountsAndIdempotency(t *testing.T) {
	s := newTestStore(t)
	book := BookRef{Slug: "book_a", Title: "Book A", ResearchPath: "/books/book_a/research"}

	st, err := s.ImportSources(book, bookA)
	if err != nil {
		t.Fatal(err)
	}
	want := ImportStats{Book: "book_a", ReportNodeID: st.ReportNodeID, ReportCreated: true, Entries: 5,
		SkippedNoURL: 1, InvalidURL: 1, UniqueSources: 2, SourcesCreated: 2, EdgesCreated: 2}
	if st != want {
		t.Fatalf("first import = %+v\nwant %+v", st, want)
	}

	again, err := s.ImportSources(book, bookA)
	if err != nil {
		t.Fatal(err)
	}
	if again.ReportCreated || again.ReportNodeID != st.ReportNodeID || again.SourcesCreated != 0 ||
		again.SourcesReused != 2 || again.SourcesUpdated != 0 || again.EdgesCreated != 0 || again.EdgesExisting != 2 {
		t.Fatalf("second import not idempotent: %+v", again)
	}

	nodes, _ := s.ListNodes(NodeSource)
	edges, _ := s.ListEdges(EdgeFilter{Type: EdgeReferences})
	if len(nodes) != 2 || len(edges) != 2 {
		t.Fatalf("nodes=%d edges=%d after two imports", len(nodes), len(edges))
	}

	report, err := s.NodeByKey(BookKey("book_a"))
	if err != nil || report.Title != "Book A" || report.Metadata["research_path"] != "/books/book_a/research" {
		t.Fatalf("report = %+v, %v", report, err)
	}

	mk, err := s.NodeByKey("url:en.wikipedia.org/wiki/Project_MKUltra")
	if err != nil {
		t.Fatal(err)
	}
	notes := stringList(mk.Metadata["notes"].(map[string]any)["book_a"])
	if len(notes) != 2 || mk.Metadata["type"] != "web" || mk.Title != "Project MKUltra" {
		t.Fatalf("merged source = %+v", mk.Metadata)
	}
	church, _ := s.NodeByKey("url:senate.gov/church")
	if church.Metadata["author"] != "US Senate" || church.Metadata["date"] != "1976" {
		t.Fatalf("church = %+v", church.Metadata)
	}
}

func TestImportSources_DedupAcrossBooks(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.ImportSources(BookRef{Slug: "book_a"}, bookA); err != nil {
		t.Fatal(err)
	}
	st, err := s.ImportSources(BookRef{Slug: "book_b"}, bookB)
	if err != nil {
		t.Fatal(err)
	}
	if st.SourcesCreated != 1 || st.SourcesReused != 1 || st.SourcesUpdated != 1 || st.EdgesCreated != 2 {
		t.Fatalf("book_b import = %+v", st)
	}

	mk, _ := s.NodeByKey("url:en.wikipedia.org/wiki/Project_MKUltra")
	books := stringList(mk.Metadata["books"])
	sort.Strings(books)
	if len(books) != 2 || books[0] != "book_a" || books[1] != "book_b" {
		t.Fatalf("books = %v", books)
	}
	notes := mk.Metadata["notes"].(map[string]any)
	if len(stringList(notes["book_a"])) != 2 || stringList(notes["book_b"])[0] != "used for chapter 2" {
		t.Fatalf("notes overwritten across books: %v", notes)
	}

	counts, err := s.CitedByCounts(EdgeReferences)
	if err != nil {
		t.Fatal(err)
	}
	if counts[mk.ID] != 2 {
		t.Fatalf("MKUltra cited by %d books, want 2", counts[mk.ID])
	}
	church, _ := s.NodeByKey("url:senate.gov/church")
	if counts[church.ID] != 1 {
		t.Fatalf("church cited by %d", counts[church.ID])
	}
	if _, err := s.NodeByKey("url:example.org/mockingbird"); err != nil {
		t.Fatalf("utm param not stripped from key: %v", err)
	}
}

func TestImportSources_Errors(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.ImportSources(BookRef{}, bookA); err == nil {
		t.Fatal("empty slug should error")
	}
	st, err := s.ImportSources(BookRef{Slug: "empty"}, nil)
	if err != nil || st.UniqueSources != 0 || !st.ReportCreated {
		t.Fatalf("empty entries: %+v, %v", st, err)
	}
}

func TestNodeKeys(t *testing.T) {
	s := newTestStore(t)
	a := &Node{Type: NodeEntity, Title: "A"}
	b := &Node{Type: NodeEntity, Title: "B"}
	must(t, s.CreateNode(a))
	must(t, s.CreateNode(b))
	if _, err := s.NodeByKey("k"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing key err = %v", err)
	}
	must(t, s.SetNodeKey("k", a.ID))
	must(t, s.SetNodeKey("k", a.ID))
	if err := s.SetNodeKey("k", b.ID); err == nil {
		t.Fatal("rebinding a key to another node should error")
	}
	got, err := s.NodeByKey("k")
	if err != nil || got.ID != a.ID {
		t.Fatalf("NodeByKey = %+v, %v", got, err)
	}
}

// The migration must run cleanly against a copy of the real tasks.db (when
// one exists on this machine) without touching existing rows.
func TestMigrate_OnCopyOfRealTasksDB(t *testing.T) {
	home, _ := os.UserHomeDir()
	real := filepath.Join(home, ".researchguy", "tasks.db")
	if _, err := os.Stat(real); err != nil {
		t.Skip("no real tasks.db on this machine")
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)
	copyPath := filepath.Join(dir, "tasks.db")
	// .backup takes a consistent snapshot even while other processes write.
	if out, err := exec.Command("sqlite3", real, ".backup '"+copyPath+"'").CombinedOutput(); err != nil {
		t.Fatalf("backing up tasks.db: %v\n%s", err, out)
	}

	countRows := func(table string) int {
		out, err := exec.Command("sqlite3", copyPath, "SELECT COUNT(*) FROM "+table).Output()
		if err != nil {
			return -1
		}
		var n int
		for _, c := range string(out) {
			if c >= '0' && c <= '9' {
				n = n*10 + int(c-'0')
			}
		}
		return n
	}
	// node_keys is -1 (no table) on a DB from before Phase 3, and holds the
	// backfilled keys on one migrated since.
	beforeNodes, beforeEdges, beforeKeys := countRows("nodes"), countRows("edges"), countRows("node_keys")

	st, err := NewStore(&config.Config{})
	if err != nil {
		t.Fatalf("opening copy: %v", err)
	}
	defer st.Close()
	if countRows("nodes") != beforeNodes || countRows("edges") != beforeEdges {
		t.Fatal("migration changed existing rows")
	}
	wantKeys := beforeKeys
	if beforeKeys < 0 {
		wantKeys = 0
	}
	if got := countRows("node_keys"); got != wantKeys {
		t.Fatalf("node_keys has %d rows after migration, want %d (before: %d)", got, wantKeys, beforeKeys)
	}
	stats, err := st.ImportSources(BookRef{Slug: "migration_test"}, bookA)
	if err != nil || stats.SourcesCreated+stats.SourcesReused != 2 {
		t.Fatalf("import on migrated copy: %+v, %v", stats, err)
	}
}

// Source nodes created before keys existed (plain add-node) are adopted on
// import instead of duplicated.
func TestImportSources_AdoptsUnkeyedSourceNodes(t *testing.T) {
	s := newTestStore(t)
	pre := &Node{Type: NodeSource, Title: "MKUltra (agent)", Metadata: map[string]any{"url": "http://www.en.wikipedia.org/wiki/Project_MKUltra/"}}
	noURL := &Node{Type: NodeSource, Title: "No URL"}
	badURL := &Node{Type: NodeSource, Title: "Bad", Metadata: map[string]any{"url": "ftp://x"}}
	must(t, s.CreateNode(pre))
	must(t, s.CreateNode(noURL))
	must(t, s.CreateNode(badURL))

	st, err := s.ImportSources(BookRef{Slug: "book_b"}, bookB)
	if err != nil {
		t.Fatal(err)
	}
	if st.SourcesReused != 1 || st.SourcesCreated != 1 {
		t.Fatalf("stats = %+v", st)
	}
	got, err := s.NodeByKey("url:en.wikipedia.org/wiki/Project_MKUltra")
	if err != nil || got.ID != pre.ID {
		t.Fatalf("pre-existing node not adopted: %+v, %v", got, err)
	}
	if nodes, _ := s.ListNodes(NodeSource); len(nodes) != 4 {
		t.Fatalf("got %d source nodes, want 4 (3 pre-existing + 1 new)", len(nodes))
	}
}
