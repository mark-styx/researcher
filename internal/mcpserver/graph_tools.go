package mcpserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/graph"
)

const defaultGraphLimit = 100

func addGraphTools(s *server.MCPServer, cfg *config.Config) {
	s.AddTool(graphListTool(), graphListHandler(cfg))
	s.AddTool(graphShowTool(), graphShowHandler(cfg))
	s.AddTool(graphFindTool(), graphFindHandler(cfg))
	s.AddTool(graphAddNodeTool(), graphAddNodeHandler(cfg))
	s.AddTool(graphAddEdgeTool(), graphAddEdgeHandler(cfg))
}

// withStore opens the graph store for one call. The store runs on the
// shared tasks.db, which tolerates concurrent writers (WAL + busy timeout).
func withStore(cfg *config.Config, fn func(*graph.Store) (*mcp.CallToolResult, error)) (*mcp.CallToolResult, error) {
	st, err := graph.NewStore(cfg)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("opening graph: %v", err)), nil
	}
	defer st.Close()
	return fn(st)
}

type nodeList struct {
	Nodes []*graph.Node `json:"nodes"`
	Count int           `json:"count"`
	Total int           `json:"total"`
}

func limitNodes(nodes []*graph.Node, limit int) nodeList {
	if nodes == nil {
		nodes = []*graph.Node{}
	}
	total := len(nodes)
	if limit > 0 && len(nodes) > limit {
		nodes = nodes[:limit]
	}
	return nodeList{Nodes: nodes, Count: len(nodes), Total: total}
}

// --- researchguy_graph_list ---

func graphListTool() mcp.Tool {
	return mcp.NewTool("researchguy_graph_list",
		mcp.WithDescription("List knowledge-graph nodes, newest first, optionally filtered by type."),
		mcp.WithString("type", mcp.Description("Node type filter"), mcp.Enum(graph.ValidNodeTypes...)),
		mcp.WithNumber("limit", mcp.Description("Max nodes to return (default 100, 0 = all)")),
	)
}

func graphListHandler(cfg *config.Config) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return withStore(cfg, func(st *graph.Store) (*mcp.CallToolResult, error) {
			nodes, err := st.ListNodes(req.GetString("type", ""))
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("listing nodes: %v", err)), nil
			}
			return toolResultJSON(limitNodes(nodes, req.GetInt("limit", defaultGraphLimit)))
		})
	}
}

// --- researchguy_graph_show ---

type edgeView struct {
	*graph.Edge
	OtherTitle string `json:"other_title,omitempty"`
	OtherType  string `json:"other_type,omitempty"`
}

type nodeView struct {
	Node     *graph.Node `json:"node"`
	Outgoing []edgeView  `json:"outgoing"`
	Incoming []edgeView  `json:"incoming"`
}

func graphShowTool() mcp.Tool {
	return mcp.NewTool("researchguy_graph_show",
		mcp.WithDescription("Show one knowledge-graph node with its outgoing and incoming edges."),
		mcp.WithString("id", mcp.Required(), mcp.Description("Node ID")),
	)
}

func graphShowHandler(cfg *config.Config) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return withStore(cfg, func(st *graph.Store) (*mcp.CallToolResult, error) {
			node, err := st.GetNode(id)
			if errors.Is(err, sql.ErrNoRows) {
				return mcp.NewToolResultError(fmt.Sprintf("node %q not found", id)), nil
			}
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("reading node: %v", err)), nil
			}
			out, err := st.ListEdges(graph.EdgeFilter{FromID: id})
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("listing edges: %v", err)), nil
			}
			in, err := st.ListEdges(graph.EdgeFilter{ToID: id})
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("listing edges: %v", err)), nil
			}
			view := nodeView{Node: node, Outgoing: []edgeView{}, Incoming: []edgeView{}}
			for _, e := range out {
				view.Outgoing = append(view.Outgoing, describeEdge(st, e, e.ToID))
			}
			for _, e := range in {
				view.Incoming = append(view.Incoming, describeEdge(st, e, e.FromID))
			}
			return toolResultJSON(view)
		})
	}
}

func describeEdge(st *graph.Store, e *graph.Edge, otherID string) edgeView {
	v := edgeView{Edge: e}
	if other, err := st.GetNode(otherID); err == nil {
		v.OtherTitle, v.OtherType = other.Title, other.Type
	}
	return v
}

// --- researchguy_graph_find ---

func graphFindTool() mcp.Tool {
	return mcp.NewTool("researchguy_graph_find",
		mcp.WithDescription("Find knowledge-graph nodes by field value: key is 'title', 'path', or any metadata key (e.g. 'url', 'book'). Matching is exact, case-insensitive for title."),
		mcp.WithString("key", mcp.Required(), mcp.Description("Field to match: title, path, or a metadata key")),
		mcp.WithString("value", mcp.Required(), mcp.Description("Value to match")),
		mcp.WithString("type", mcp.Description("Node type filter"), mcp.Enum(graph.ValidNodeTypes...)),
		mcp.WithNumber("limit", mcp.Description("Max nodes to return (default 100, 0 = all)")),
	)
}

func graphFindHandler(cfg *config.Config) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		key, err := req.RequireString("key")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		value, err := req.RequireString("value")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return withStore(cfg, func(st *graph.Store) (*mcp.CallToolResult, error) {
			nodes, err := st.ListNodes(req.GetString("type", ""))
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("listing nodes: %v", err)), nil
			}
			var matched []*graph.Node
			for _, n := range nodes {
				if nodeMatches(n, key, value) {
					matched = append(matched, n)
				}
			}
			return toolResultJSON(limitNodes(matched, req.GetInt("limit", defaultGraphLimit)))
		})
	}
}

func nodeMatches(n *graph.Node, key, value string) bool {
	switch key {
	case "title":
		return strings.EqualFold(n.Title, value)
	case "path":
		return n.Path == value
	}
	v, ok := n.Metadata[key]
	if !ok {
		return false
	}
	switch tv := v.(type) {
	case string:
		return tv == value
	case []any:
		for _, item := range tv {
			if fmt.Sprint(item) == value {
				return true
			}
		}
		return false
	default:
		return fmt.Sprint(tv) == value
	}
}

// --- researchguy_graph_add_node ---

func graphAddNodeTool() mcp.Tool {
	return mcp.NewTool("researchguy_graph_add_node",
		mcp.WithDescription("Create a knowledge-graph node. Returns the created node with its ID."),
		mcp.WithString("type", mcp.Required(), mcp.Description("Node type"), mcp.Enum(graph.ValidNodeTypes...)),
		mcp.WithString("title", mcp.Required(), mcp.Description("Node title")),
		mcp.WithString("path", mcp.Description("Markdown content path, relative to research_dir")),
		mcp.WithString("summary", mcp.Description("Short summary shown in listings")),
		mcp.WithObject("metadata", mcp.Description("Free-form metadata (e.g. {\"url\": \"...\"})")),
	)
}

func graphAddNodeHandler(cfg *config.Config) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		nodeType, err := req.RequireString("type")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		title, err := req.RequireString("title")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		meta, err := objectArg(req, "metadata")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		n := &graph.Node{
			Type:     nodeType,
			Title:    title,
			Path:     req.GetString("path", ""),
			Summary:  req.GetString("summary", ""),
			Metadata: meta,
		}
		return withStore(cfg, func(st *graph.Store) (*mcp.CallToolResult, error) {
			if err := st.CreateNode(n); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("creating node: %v", err)), nil
			}
			return toolResultJSON(n)
		})
	}
}

// --- researchguy_graph_add_edge ---

func graphAddEdgeTool() mcp.Tool {
	return mcp.NewTool("researchguy_graph_add_edge",
		mcp.WithDescription("Create a typed edge between two existing knowledge-graph nodes."),
		mcp.WithString("from", mcp.Required(), mcp.Description("Source node ID")),
		mcp.WithString("to", mcp.Required(), mcp.Description("Target node ID")),
		mcp.WithString("type", mcp.Required(), mcp.Description("Edge type"), mcp.Enum(graph.ValidEdgeTypes...)),
		mcp.WithObject("metadata", mcp.Description("Free-form edge metadata")),
	)
}

func graphAddEdgeHandler(cfg *config.Config) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var fields [3]string
		for i, name := range []string{"from", "to", "type"} {
			v, err := req.RequireString(name)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			fields[i] = v
		}
		meta, err := objectArg(req, "metadata")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		e := &graph.Edge{FromID: fields[0], ToID: fields[1], Type: fields[2], Metadata: meta}
		return withStore(cfg, func(st *graph.Store) (*mcp.CallToolResult, error) {
			if err := st.CreateEdge(e); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("creating edge: %v", err)), nil
			}
			return toolResultJSON(e)
		})
	}
}

// objectArg returns an optional object argument as a map.
func objectArg(req mcp.CallToolRequest, name string) (map[string]any, error) {
	raw, ok := req.GetArguments()[name]
	if !ok || raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", name)
	}
	return m, nil
}
