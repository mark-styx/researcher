package mcpserver

import (
	"strings"
	"testing"

	"github.com/marklubin/researchguy/internal/graph"
)

func TestGraphTools_RoundTrip(t *testing.T) {
	t.Setenv("RESEARCHGUY_CONFIG_DIR", t.TempDir())
	cfg := testConfig(t)
	p := &mockProvider{}

	var book, src graph.Node
	decode(t, call(t, cfg, p, "researchguy_graph_add_node", map[string]any{
		"type": "report", "title": "The Poisoned Well", "metadata": map[string]any{"book": "the_poisoned_well"},
	}), &book)
	decode(t, call(t, cfg, p, "researchguy_graph_add_node", map[string]any{
		"type": "source", "title": "Project MKUltra", "summary": "wiki",
		"metadata": map[string]any{"url": "https://en.wikipedia.org/wiki/Project_MKUltra", "books": []any{"a", "b"}},
	}), &src)
	if book.ID == "" || src.ID == "" {
		t.Fatal("ids not assigned")
	}

	var edge graph.Edge
	decode(t, call(t, cfg, p, "researchguy_graph_add_edge", map[string]any{
		"from": book.ID, "to": src.ID, "type": "references",
	}), &edge)
	if edge.ID == "" || edge.FromID != book.ID {
		t.Fatalf("edge = %+v", edge)
	}

	var view nodeView
	decode(t, call(t, cfg, p, "researchguy_graph_show", map[string]any{"id": src.ID}), &view)
	if len(view.Incoming) != 1 || view.Incoming[0].OtherTitle != "The Poisoned Well" || len(view.Outgoing) != 0 {
		t.Fatalf("view = %+v", view)
	}

	var list nodeList
	decode(t, call(t, cfg, p, "researchguy_graph_list", map[string]any{"type": "source"}), &list)
	if list.Count != 1 || list.Total != 1 || list.Nodes[0].ID != src.ID {
		t.Fatalf("list = %+v", list)
	}
	decode(t, call(t, cfg, p, "researchguy_graph_list", map[string]any{"limit": 1}), &list)
	if list.Count != 1 || list.Total != 2 {
		t.Fatalf("limited list = %+v", list)
	}

	for _, tc := range []struct {
		args map[string]any
		want int
	}{
		{map[string]any{"key": "url", "value": "https://en.wikipedia.org/wiki/Project_MKUltra"}, 1},
		{map[string]any{"key": "books", "value": "b"}, 1},
		{map[string]any{"key": "title", "value": "project mkultra"}, 1},
		{map[string]any{"key": "book", "value": "the_poisoned_well", "type": "source"}, 0},
		{map[string]any{"key": "missing", "value": "x"}, 0},
	} {
		var found nodeList
		decode(t, call(t, cfg, p, "researchguy_graph_find", tc.args), &found)
		if found.Count != tc.want || found.Nodes == nil {
			t.Errorf("find %v = %d nodes, want %d", tc.args, found.Count, tc.want)
		}
	}
	if len(p.calls) != 0 {
		t.Fatal("graph tools must not call the provider")
	}
}

func TestGraphTools_Errors(t *testing.T) {
	t.Setenv("RESEARCHGUY_CONFIG_DIR", t.TempDir())
	cfg := testConfig(t)
	for name, tc := range map[string]struct {
		tool string
		args map[string]any
		want string
	}{
		"bad node type":    {"researchguy_graph_add_node", map[string]any{"type": "person", "title": "x"}, "invalid node type"},
		"metadata not obj": {"researchguy_graph_add_node", map[string]any{"type": "entity", "title": "x", "metadata": "nope"}, "must be an object"},
		"missing endpoint": {"researchguy_graph_add_edge", map[string]any{"from": "a", "to": "b", "type": "references"}, "not found"},
		"show missing":     {"researchguy_graph_show", map[string]any{"id": "nope"}, "not found"},
		"find no key":      {"researchguy_graph_find", map[string]any{"value": "x"}, "key"},
	} {
		t.Run(name, func(t *testing.T) {
			res := call(t, cfg, &mockProvider{}, tc.tool, tc.args)
			if !res.IsError || !strings.Contains(extractText(t, res), tc.want) {
				t.Fatalf("got %q (error=%v), want error containing %q", extractText(t, res), res.IsError, tc.want)
			}
		})
	}
}
