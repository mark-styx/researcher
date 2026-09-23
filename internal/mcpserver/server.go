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
	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/research"
	"github.com/marklubin/researchguy/internal/search"
)

// New creates an MCP server with all researchguy tools registered.
func New(cfg *config.Config, provider llm.Provider, version string) *server.MCPServer {
	s := server.NewMCPServer(
		"Researchguy",
		version,
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)

	s.AddTool(askTool(), askHandler(cfg, provider))
	s.AddTool(diveTool(), diveHandler(cfg, provider))
	s.AddTool(reviewTool(), reviewHandler(cfg, provider))
	s.AddTool(compareTool(), compareHandler(cfg, provider))
	s.AddTool(enrichTool(), enrichHandler(cfg, provider))
	s.AddTool(searchTool(), searchHandler(cfg))
	s.AddTool(contextTool(), contextHandler(cfg))
	s.AddTool(listTool(), listHandler(cfg))
	s.AddTool(readTool(), readHandler(cfg))

	return s
}

// --- researchguy_ask ---

func askTool() mcp.Tool {
	return mcp.NewTool("researchguy_ask",
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

// --- researchguy_dive ---

func diveTool() mcp.Tool {
	return mcp.NewTool("researchguy_dive",
		mcp.WithDescription("Generate a comprehensive deep-dive research report on a topic. Saves to research directory."),
		mcp.WithString("topic", mcp.Required(), mcp.Description("The topic to research")),
		mcp.WithBoolean("no_research", mcp.Description("Skip searching existing research for context")),
		mcp.WithString("max_age", mcp.Description("Max age for research freshness filter (e.g. 90d, 2w, 24h)")),
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
			Type:       research.TypeDive,
			Topic:      topic,
			Quiet:      true,
			NoResearch: req.GetBool("no_research", false),
			MaxAge:     req.GetString("max_age", ""),
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

// --- researchguy_review ---

func reviewTool() mcp.Tool {
	return mcp.NewTool("researchguy_review",
		mcp.WithDescription("Create a literature review / synthesis on a topic. Optionally provide source file paths."),
		mcp.WithString("topic", mcp.Required(), mcp.Description("The topic to review")),
		mcp.WithString("sources", mcp.Description("Comma-separated file paths to include as source material")),
		mcp.WithBoolean("no_research", mcp.Description("Skip searching existing research for context")),
		mcp.WithString("max_age", mcp.Description("Max age for research freshness filter (e.g. 90d, 2w, 24h)")),
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
			for _, p := range strings.Split(s, ",") {
				if p = strings.TrimSpace(p); p != "" {
					sources = append(sources, p)
				}
			}
		}

		runner := research.NewRunner(cfg, provider)
		result, err := runner.Run(ctx, research.Task{
			Type:       research.TypeReview,
			Topic:      topic,
			Sources:    sources,
			Quiet:      true,
			NoResearch: req.GetBool("no_research", false),
			MaxAge:     req.GetString("max_age", ""),
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

// --- researchguy_compare ---

func compareTool() mcp.Tool {
	return mcp.NewTool("researchguy_compare",
		mcp.WithDescription("Side-by-side comparative analysis of two topics or two existing documents."),
		mcp.WithString("subject1", mcp.Required(), mcp.Description("First subject/topic to compare")),
		mcp.WithString("subject2", mcp.Required(), mcp.Description("Second subject/topic to compare")),
		mcp.WithString("sources", mcp.Description("Comma-separated paths to two existing documents to compare instead of topics")),
		mcp.WithBoolean("no_research", mcp.Description("Skip searching existing research for context")),
		mcp.WithString("max_age", mcp.Description("Max age for research freshness filter (e.g. 90d, 2w, 24h)")),
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
			for _, p := range strings.Split(s, ",") {
				if p = strings.TrimSpace(p); p != "" {
					sources = append(sources, p)
				}
			}
		}

		runner := research.NewRunner(cfg, provider)
		result, err := runner.Run(ctx, research.Task{
			Type:       research.TypeCompare,
			Topic:      topic,
			Sources:    sources,
			Quiet:      true,
			NoResearch: req.GetBool("no_research", false),
			MaxAge:     req.GetString("max_age", ""),
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

// --- researchguy_enrich ---

func enrichTool() mcp.Tool {
	return mcp.NewTool("researchguy_enrich",
		mcp.WithDescription("Expand and add context to an existing research document. Produces an enriched version saved alongside the original."),
		mcp.WithString("path", mcp.Required(), mcp.Description("Path to the document to enrich (relative to research dir, e.g. 'llm/agents.md', or absolute)")),
	)
}

func enrichHandler(cfg *config.Config, provider llm.Provider) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		docPath, err := req.RequireString("path")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// If path is relative, resolve against research dir
		if !filepath.IsAbs(docPath) {
			researchDir := config.ExpandPath(cfg.ResearchDir)
			docPath = filepath.Join(researchDir, docPath)
		}

		runner := research.NewRunner(cfg, provider)
		result, err := runner.Run(ctx, research.Task{
			Type:    research.TypeEnrich,
			Topic:   docPath,
			Sources: []string{docPath},
			Quiet:   true,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("enrich failed: %v", err)), nil
		}

		return toolResultJSON(map[string]string{
			"enriched": result.Response,
			"saved_to": result.FilePath,
		})
	}
}

// --- researchguy_search ---

func searchTool() mcp.Tool {
	return mcp.NewTool("researchguy_search",
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

// --- researchguy_context ---

type contextSource struct {
	FilePath  string  `json:"file_path"`
	Score     float64 `json:"score"`
	Freshness string  `json:"freshness"`
	Modified  string  `json:"modified"`
	Excerpt   string  `json:"excerpt"`
}

type contextResult struct {
	Topic   string          `json:"topic"`
	Sources []contextSource `json:"sources"`
	Context string          `json:"context"`
	Count   int             `json:"count"`
}

func contextTool() mcp.Tool {
	return mcp.NewTool("researchguy_context",
		mcp.WithDescription("Get pre-formatted research context for a topic. Returns relevant excerpts with source files and freshness metadata. Does not trigger an LLM call — purely retrieves and formats existing research."),
		mcp.WithString("topic", mcp.Required(), mcp.Description("The topic to gather context for")),
		mcp.WithString("max_age", mcp.Description("Max age for freshness filter (e.g. 90d, 2w, 24h). Default: from config or 90d")),
		mcp.WithNumber("limit", mcp.Description("Max search results to include (default 10)")),
	)
}

func contextHandler(cfg *config.Config) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		topic, err := req.RequireString("topic")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		maxAgeStr := req.GetString("max_age", cfg.Ask.MaxAge)
		if maxAgeStr == "" {
			maxAgeStr = "90d"
		}
		maxAge, err := search.ParseMaxAge(maxAgeStr)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid max_age: %v", err)), nil
		}

		limit := req.GetInt("limit", 10)

		results, err := search.QueryJSON(cfg, topic, limit)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
		}

		empty := contextResult{Topic: topic, Sources: []contextSource{}, Context: "", Count: 0}
		if len(results) == 0 {
			return toolResultJSON(empty)
		}

		researchDir := config.ExpandPath(cfg.ResearchDir)
		fresh := search.FilterFresh(results, researchDir, maxAge)
		if len(fresh) == 0 {
			return toolResultJSON(empty)
		}

		contents := search.ReadContents(fresh, researchDir)
		formatted := search.FormatContext(contents)

		// Build structured source metadata
		seen := make(map[string]bool)
		var sources []contextSource
		for _, r := range fresh {
			path := r.FilePath
			if !filepath.IsAbs(path) {
				path = filepath.Join(researchDir, path)
			}
			if seen[path] {
				continue
			}
			seen[path] = true

			var freshness, modified string
			if info, err := os.Stat(path); err == nil {
				freshness = search.FreshnessLabel(info.ModTime())
				modified = info.ModTime().Format("2006-01-02")
			}

			// Use grepai excerpt (truncated to keep response size reasonable)
			excerpt := r.Content
			if len(excerpt) > 500 {
				excerpt = excerpt[:500] + "..."
			}

			sources = append(sources, contextSource{
				FilePath:  r.FilePath,
				Score:     r.Score,
				Freshness: freshness,
				Modified:  modified,
				Excerpt:   excerpt,
			})
		}

		return toolResultJSON(contextResult{
			Topic:   topic,
			Sources: sources,
			Context: formatted,
			Count:   len(sources),
		})
	}
}

// --- researchguy_list ---

func listTool() mcp.Tool {
	return mcp.NewTool("researchguy_list",
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

// --- researchguy_read ---

func readTool() mcp.Tool {
	return mcp.NewTool("researchguy_read",
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

		// Resolve through symlinks before boundary check.
		resolvedPath, err := filepath.EvalSymlinks(fullPath)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("reading file: %v", err)), nil
		}

		// Verify the resolved path is still within the research directory.
		resolvedResearchDir, err := filepath.EvalSymlinks(researchDir)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("resolving research directory: %v", err)), nil
		}
		absResearch, err := filepath.Abs(resolvedResearchDir)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("resolving research directory: %v", err)), nil
		}
		absFile, err := filepath.Abs(resolvedPath)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("resolving file path: %v", err)), nil
		}
		rel, err := filepath.Rel(absResearch, absFile)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("resolving relative path: %v", err)), nil
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return mcp.NewToolResultError("path must be within the research directory"), nil
		}

		data, err := os.ReadFile(absFile)
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
