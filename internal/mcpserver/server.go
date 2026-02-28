package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/marklubin/researcher/internal/config"
	"github.com/marklubin/researcher/internal/llm"
	"github.com/marklubin/researcher/internal/research"
	"github.com/marklubin/researcher/internal/search"
)

// New creates an MCP server with all researcher tools registered.
func New(cfg *config.Config, provider llm.Provider, version string) *server.MCPServer {
	s := server.NewMCPServer(
		"Researcher",
		version,
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)

	s.AddTool(askTool(), askHandler(cfg, provider))
	s.AddTool(diveTool(), diveHandler(cfg, provider))
	s.AddTool(reviewTool(), reviewHandler(cfg, provider))
	s.AddTool(compareTool(), compareHandler(cfg, provider))
	s.AddTool(searchTool(), searchHandler(cfg))
	s.AddTool(listTool(), listHandler(cfg))
	s.AddTool(readTool(), readHandler(cfg))

	return s
}

// --- researcher_ask ---

func askTool() mcp.Tool {
	return mcp.NewTool("researcher_ask",
		mcp.WithDescription("Ask a question, optionally informed by existing research. Returns the answer and saves it."),
		mcp.WithString("question", mcp.Required(), mcp.Description("The question to ask")),
		mcp.WithBoolean("no_research", mcp.Description("Skip searching existing research for context")),
		mcp.WithBoolean("no_save", mcp.Description("Don't save the answer to the research directory")),
		mcp.WithString("max_age", mcp.Description("Max age for research freshness filter (e.g. 90d, 2w, 24h)")),
	)
}

func askHandler(cfg *config.Config, provider llm.Provider) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		question, err := req.RequireString("question")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		task := research.Task{
			Type:       research.TypeAsk,
			Topic:      question,
			Quiet:      true,
			NoResearch: req.GetBool("no_research", false),
			NoSave:     req.GetBool("no_save", false),
			MaxAge:     req.GetString("max_age", ""),
		}

		runner := research.NewRunner(cfg, provider)
		result, err := runner.Run(ctx, task)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("ask failed: %v", err)), nil
		}

		return toolResultJSON(map[string]string{
			"answer":   result.Response,
			"saved_to": result.FilePath,
		})
	}
}

// --- researcher_dive ---

func diveTool() mcp.Tool {
	return mcp.NewTool("researcher_dive",
		mcp.WithDescription("Generate a comprehensive deep-dive research report on a topic. Saves to research directory."),
		mcp.WithString("topic", mcp.Required(), mcp.Description("The topic to research")),
	)
}

func diveHandler(cfg *config.Config, provider llm.Provider) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		topic, err := req.RequireString("topic")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		runner := research.NewRunner(cfg, provider)
		result, err := runner.Run(ctx, research.Task{
			Type:  research.TypeDive,
			Topic: topic,
			Quiet: true,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("dive failed: %v", err)), nil
		}

		return toolResultJSON(map[string]string{
			"report":   result.Response,
			"saved_to": result.FilePath,
		})
	}
}

// --- researcher_review ---

func reviewTool() mcp.Tool {
	return mcp.NewTool("researcher_review",
		mcp.WithDescription("Create a literature review / synthesis on a topic. Optionally provide source file paths."),
		mcp.WithString("topic", mcp.Required(), mcp.Description("The topic to review")),
		mcp.WithString("sources", mcp.Description("Comma-separated file paths to include as source material")),
	)
}

func reviewHandler(cfg *config.Config, provider llm.Provider) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		topic, err := req.RequireString("topic")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		var sources []string
		if s := req.GetString("sources", ""); s != "" {
			sources = strings.Split(s, ",")
		}

		runner := research.NewRunner(cfg, provider)
		result, err := runner.Run(ctx, research.Task{
			Type:    research.TypeReview,
			Topic:   topic,
			Sources: sources,
			Quiet:   true,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("review failed: %v", err)), nil
		}

		return toolResultJSON(map[string]string{
			"review":   result.Response,
			"saved_to": result.FilePath,
		})
	}
}

// --- researcher_compare ---

func compareTool() mcp.Tool {
	return mcp.NewTool("researcher_compare",
		mcp.WithDescription("Side-by-side comparative analysis of two topics or two existing documents."),
		mcp.WithString("subject1", mcp.Required(), mcp.Description("First subject/topic to compare")),
		mcp.WithString("subject2", mcp.Required(), mcp.Description("Second subject/topic to compare")),
		mcp.WithString("sources", mcp.Description("Comma-separated paths to two existing documents to compare instead of topics")),
	)
}

func compareHandler(cfg *config.Config, provider llm.Provider) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		subject1, err := req.RequireString("subject1")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		subject2, err := req.RequireString("subject2")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		topic := subject1 + " vs " + subject2

		var sources []string
		if s := req.GetString("sources", ""); s != "" {
			sources = strings.Split(s, ",")
		}

		runner := research.NewRunner(cfg, provider)
		result, err := runner.Run(ctx, research.Task{
			Type:    research.TypeCompare,
			Topic:   topic,
			Sources: sources,
			Quiet:   true,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("compare failed: %v", err)), nil
		}

		return toolResultJSON(map[string]string{
			"comparison": result.Response,
			"saved_to":   result.FilePath,
		})
	}
}

// --- researcher_search ---

func searchTool() mcp.Tool {
	return mcp.NewTool("researcher_search",
		mcp.WithDescription("Semantic search across research documents via grepai. Returns ranked results."),
		mcp.WithString("query", mcp.Required(), mcp.Description("The search query")),
		mcp.WithNumber("limit", mcp.Description("Max results to return (default 10)")),
		mcp.WithString("max_age", mcp.Description("Filter results by freshness (e.g. 90d, 2w, 24h)")),
	)
}

func searchHandler(cfg *config.Config) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		limit := req.GetInt("limit", 10)

		results, err := search.QueryJSON(cfg, query, limit)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
		}

		// Apply freshness filter if max_age is set
		if maxAgeStr := req.GetString("max_age", ""); maxAgeStr != "" {
			maxAge, err := search.ParseMaxAge(maxAgeStr)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid max_age: %v", err)), nil
			}
			researchDir := config.ExpandPath(cfg.ResearchDir)
			results = search.FilterFresh(results, researchDir, maxAge)
		}

		return toolResultJSON(results)
	}
}

// --- researcher_list ---

func listTool() mcp.Tool {
	return mcp.NewTool("researcher_list",
		mcp.WithDescription("List research files grouped by category."),
		mcp.WithString("category", mcp.Description("Filter to a specific category")),
	)
}

type listEntry struct {
	Category string `json:"category"`
	File     string `json:"file"`
	Modified string `json:"modified"`
}

func listHandler(cfg *config.Config) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		researchDir := config.ExpandPath(cfg.ResearchDir)
		filterCat := req.GetString("category", "")

		entries, err := os.ReadDir(researchDir)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("reading research dir: %v", err)), nil
		}

		var files []listEntry
		for _, e := range entries {
			if !e.IsDir() || e.Name()[0] == '.' {
				continue
			}
			if filterCat != "" && e.Name() != filterCat {
				continue
			}

			catDir := filepath.Join(researchDir, e.Name())
			catFiles, err := os.ReadDir(catDir)
			if err != nil {
				continue
			}

			for _, f := range catFiles {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
					continue
				}
				info, err := f.Info()
				modified := ""
				if err == nil {
					modified = info.ModTime().Format("2006-01-02")
				}
				files = append(files, listEntry{
					Category: e.Name(),
					File:     strings.TrimSuffix(f.Name(), ".md"),
					Modified: modified,
				})
			}
		}

		sort.Slice(files, func(i, j int) bool {
			if files[i].Category != files[j].Category {
				return files[i].Category < files[j].Category
			}
			return files[i].File < files[j].File
		})

		return toolResultJSON(files)
	}
}

// --- researcher_read ---

func readTool() mcp.Tool {
	return mcp.NewTool("researcher_read",
		mcp.WithDescription("Read a research document. Path is relative to the research directory (e.g. 'llm/agents.md')."),
		mcp.WithString("path", mcp.Required(), mcp.Description("Relative path to the file (e.g. llm/agents.md)")),
	)
}

func readHandler(cfg *config.Config) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		relPath, err := req.RequireString("path")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		researchDir := config.ExpandPath(cfg.ResearchDir)

		// Prevent directory traversal
		cleaned := filepath.Clean(relPath)
		if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
			return mcp.NewToolResultError("path must be relative to the research directory"), nil
		}

		fullPath := filepath.Join(researchDir, cleaned)

		// Verify the resolved path is still within the research directory
		absResearch, _ := filepath.Abs(researchDir)
		absFile, _ := filepath.Abs(fullPath)
		if !strings.HasPrefix(absFile, absResearch) {
			return mcp.NewToolResultError("path must be within the research directory"), nil
		}

		data, err := os.ReadFile(fullPath)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("reading file: %v", err)), nil
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolResultJSON marshals v to JSON and returns it as a text tool result.
func toolResultJSON(v any) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling result: %w", err)
	}
	return mcp.NewToolResultText(string(data)), nil
}
