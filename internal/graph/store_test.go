package graph

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrating test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Store{db: db}
}

func TestMigrate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	defer db.Close()

	if err := migrate(db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestCreateNode_AssignsIDAndTimestamps(t *testing.T) {
	s := newTestStore(t)
	n := &Node{Type: NodeEntity, Title: "Acme Corp"}
	if err := s.CreateNode(n); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if n.ID == "" {
		t.Error("expected ID to be assigned")
	}
	if n.CreatedAt.IsZero() || n.UpdatedAt.IsZero() {
		t.Error("expected timestamps to be assigned")
	}
}

func TestCreateNode_InvalidType(t *testing.T) {
	s := newTestStore(t)
	err := s.CreateNode(&Node{Type: "bogus", Title: "x"})
	if err == nil {
		t.Fatal("expected error for invalid node type")
	}
	if !strings.Contains(err.Error(), "invalid node type") {
		t.Errorf("error = %q, want to contain 'invalid node type'", err.Error())
	}
}

func TestCreateNode_MissingTitle(t *testing.T) {
	s := newTestStore(t)
	err := s.CreateNode(&Node{Type: NodeEntity})
	if err == nil {
		t.Fatal("expected error for missing title")
	}
}

func TestGetNode_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetNode("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent node")
	}
}

func TestGetNode_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	n := &Node{
		Type:    NodeFundingPattern,
		Title:   "Org X funds Study Y",
		Path:    "funding/org-x.md",
		Summary: "Org X sponsored Study Y, which found results aligned with Org X's stated position.",
		Metadata: map[string]any{
			"sufficiency": "insufficient evidence of intent, correlation only",
		},
	}
	if err := s.CreateNode(n); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}

	got, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Title != n.Title {
		t.Errorf("Title = %q, want %q", got.Title, n.Title)
	}
	if got.Path != n.Path {
		t.Errorf("Path = %q, want %q", got.Path, n.Path)
	}
	if got.Summary != n.Summary {
		t.Errorf("Summary = %q, want %q", got.Summary, n.Summary)
	}
	if got.Metadata["sufficiency"] != n.Metadata["sufficiency"] {
		t.Errorf("Metadata[sufficiency] = %v, want %v", got.Metadata["sufficiency"], n.Metadata["sufficiency"])
	}
}

func TestListNodes_FilterByType(t *testing.T) {
	s := newTestStore(t)
	must(t, s.CreateNode(&Node{Type: NodeEntity, Title: "Entity A"}))
	must(t, s.CreateNode(&Node{Type: NodeSource, Title: "Source A"}))
	must(t, s.CreateNode(&Node{Type: NodeEntity, Title: "Entity B"}))

	entities, err := s.ListNodes(NodeEntity)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(entities) != 2 {
		t.Fatalf("len = %d, want 2", len(entities))
	}
	for _, e := range entities {
		if e.Type != NodeEntity {
			t.Errorf("got type %q in entity filter", e.Type)
		}
	}
}

func TestListNodes_All(t *testing.T) {
	s := newTestStore(t)
	must(t, s.CreateNode(&Node{Type: NodeEntity, Title: "A"}))
	must(t, s.CreateNode(&Node{Type: NodeSource, Title: "B"}))

	all, err := s.ListNodes("")
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len = %d, want 2", len(all))
	}
}

func TestUpdateNode_BumpsUpdatedAt(t *testing.T) {
	s := newTestStore(t)
	n := &Node{Type: NodeClaim, Title: "Original title", Summary: "v1"}
	must(t, s.CreateNode(n))
	firstUpdated := n.UpdatedAt

	n.Summary = "v2, revised after new evidence arrived"
	if err := s.UpdateNode(n); err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}

	got, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Summary != "v2, revised after new evidence arrived" {
		t.Errorf("Summary = %q, want updated value", got.Summary)
	}
	if !got.UpdatedAt.After(firstUpdated) && got.UpdatedAt != firstUpdated {
		// allow equal on fast clocks, but not before
		if got.UpdatedAt.Before(firstUpdated) {
			t.Errorf("UpdatedAt went backwards: %v -> %v", firstUpdated, got.UpdatedAt)
		}
	}
}

func TestUpdateNode_NotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.UpdateNode(&Node{ID: "nonexistent", Title: "x"})
	if err == nil {
		t.Fatal("expected error updating nonexistent node")
	}
}

func TestCreateEdge_Success(t *testing.T) {
	s := newTestStore(t)
	from := &Node{Type: NodeClaim, Title: "Claim A"}
	to := &Node{Type: NodeSource, Title: "Source A"}
	must(t, s.CreateNode(from))
	must(t, s.CreateNode(to))

	e := &Edge{FromID: from.ID, ToID: to.ID, Type: EdgeSupports}
	if err := s.CreateEdge(e); err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}
	if e.ID == "" {
		t.Error("expected edge ID to be assigned")
	}
}

func TestCreateEdge_InvalidType(t *testing.T) {
	s := newTestStore(t)
	from := &Node{Type: NodeClaim, Title: "A"}
	to := &Node{Type: NodeSource, Title: "B"}
	must(t, s.CreateNode(from))
	must(t, s.CreateNode(to))

	err := s.CreateEdge(&Edge{FromID: from.ID, ToID: to.ID, Type: "bogus"})
	if err == nil {
		t.Fatal("expected error for invalid edge type")
	}
}

func TestCreateEdge_MissingEndpoint(t *testing.T) {
	s := newTestStore(t)
	from := &Node{Type: NodeClaim, Title: "A"}
	must(t, s.CreateNode(from))

	err := s.CreateEdge(&Edge{FromID: from.ID, ToID: "nonexistent", Type: EdgeSupports})
	if err == nil {
		t.Fatal("expected error for nonexistent endpoint")
	}
}

func TestListEdges_Filters(t *testing.T) {
	s := newTestStore(t)
	a := &Node{Type: NodeEntity, Title: "A"}
	b := &Node{Type: NodeEntity, Title: "B"}
	c := &Node{Type: NodeEntity, Title: "C"}
	must(t, s.CreateNode(a))
	must(t, s.CreateNode(b))
	must(t, s.CreateNode(c))

	must(t, s.CreateEdge(&Edge{FromID: a.ID, ToID: b.ID, Type: EdgeFunds}))
	must(t, s.CreateEdge(&Edge{FromID: a.ID, ToID: c.ID, Type: EdgeSponsorsResearch}))
	must(t, s.CreateEdge(&Edge{FromID: c.ID, ToID: b.ID, Type: EdgeContradicts}))

	fromA, err := s.ListEdges(EdgeFilter{FromID: a.ID})
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	if len(fromA) != 2 {
		t.Fatalf("fromA len = %d, want 2", len(fromA))
	}

	toB, err := s.ListEdges(EdgeFilter{ToID: b.ID})
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	if len(toB) != 2 {
		t.Fatalf("toB len = %d, want 2", len(toB))
	}

	fundsOnly, err := s.ListEdges(EdgeFilter{Type: EdgeFunds})
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	if len(fundsOnly) != 1 {
		t.Fatalf("fundsOnly len = %d, want 1", len(fundsOnly))
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
