package mcpserver

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/tools"
)

// WebTools are the tools NewWeb registers.
var WebTools = []string{"web_search", "web_fetch"}

type (
	searchFunc func(ctx context.Context, query string) ([]tools.SearchResult, error)
	fetchFunc  func(ctx context.Context, url string) (string, error)
)

// NewWeb creates an MCP server with only web_search and web_fetch, the same
// keyless search and page fetch the Ollama workers call. It's what a Goose
// worker gets (`researchguy mcp --profile web`): it can search and read
// pages, but can't read or write the store or the graph, or start research.
func NewWeb(cfg *config.Config, version string) *server.MCPServer {
	maxResults := cfg.Tools.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}
	return newWeb(version,
		func(ctx context.Context, query string) ([]tools.SearchResult, error) {
			return tools.WebSearch(ctx, query, maxResults)
		},
		tools.WebFetch,
	)
}

func newWeb(version string, search searchFunc, fetch fetchFunc) *server.MCPServer {
	s := newServer(version)
	s.AddTool(webSearchTool(), webSearchHandler(search))
	s.AddTool(webFetchTool(), webFetchHandler(fetch))
	return s
}

func webSearchTool() mcp.Tool {
	return mcp.NewTool("web_search",
		mcp.WithDescription("Search the web (DuckDuckGo). Returns ranked results with titles, URLs, and snippets."),
		mcp.WithString("query", mcp.Required(), mcp.Description("The search query")),
	)
}

// webSearchResult is a search result as the structured tool result carries
// it, so a client can rank results without parsing the text.
type webSearchResult struct {
	Rank    int    `json:"rank"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

func webSearchHandler(search searchFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		results, err := search(ctx, query)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
		}
		out := make([]webSearchResult, len(results))
		for i, r := range results {
			out[i] = webSearchResult{Rank: i + 1, Title: r.Title, URL: r.URL, Snippet: r.Snippet}
		}
		return mcp.NewToolResultStructured(map[string]any{"results": out}, tools.FormatSearchResults(results)), nil
	}
}

func webFetchTool() mcp.Tool {
	return mcp.NewTool("web_fetch",
		mcp.WithDescription("Fetch a web page and return its extracted text."),
		mcp.WithString("url", mcp.Required(), mcp.Description("The URL to fetch")),
	)
}

func webFetchHandler(fetch fetchFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		url, err := req.RequireString("url")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		text, err := fetch(ctx, url)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("fetch failed: %v", err)), nil
		}
		return mcp.NewToolResultText(text), nil
	}
}
