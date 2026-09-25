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
	s.AddTool(critiqueTool(), critiqueHandler(cfg, provider))
	s.AddTool(searchTool(), searchHandler(cfg))
	s.AddTool(contextTool(), contextHandler(cfg))
	s.AddTool(listTool(), listHandler(cfg))
	s.AddTool(readTool(), readHandler(cfg))
	addGraphTools(s, cfg)

	return s
}

// --- researchguy_ask ---

func askTool() mcp.Tool {
	return mcp.NewTool("researchguy_ask",
		mcp.WithDescription("Ask a question, optionally informed by existing research. Returns the answer and saves it."),
		mcp.WithString("question", mcp.Required(), mcp.Description("The question to ask")),
		mcp.WithBoolean("no_research", mcp.Description("Skip searching existing research for context")),
		mcp.WithBoolean("no_save", mcp.Description("Don't save the answer to the research directory")),
		mcp.WithString("max_age", mcp.Description("Max age for research freshness filter (e.g. 90d, 2w, 24h, or none)")),
		projectsOption(),
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
			Projects:   req.GetStringSlice("projects", nil),
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
	opts := []mcp.ToolOption{
		mcp.WithDescription("Generate a comprehensive deep-dive research report on a topic. Saves to research directory. With backend=hybrid, mode=inquiry adds counter-evidence and funding-provenance branches plus critic notes."),
		mcp.WithString("topic", mcp.Required(), mcp.Description("The topic to research")),
		mcp.WithBoolean("no_research", mcp.Description("Skip searching existing research for context")),
		mcp.WithString("max_age", mcp.Description("Max age for research freshness filter (e.g. 90d, 2w, 24h, or none)")),
	}
	return mcp.NewTool("researchguy_dive", append(opts, hybridOptions()...)...)
}

func diveHandler(cfg *config.Config, provider llm.Provider) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		topic, err := req.RequireString("topic")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		p, err := parseResearchParams(cfg, provider, req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		runner := research.NewRunner(cfg, p.provider)
		result, err := runner.Run(ctx, research.Task{
			Type:        research.TypeDive,
			Topic:       topic,
			Quiet:       true,
			NoResearch:  req.GetBool("no_research", false),
			MaxAge:      req.GetString("max_age", ""),
			Mode:        p.mode,
			BranchCount: p.branches,
			Projects:    p.projects,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("dive failed: %v", err)), nil
		}

		return toolResultJSON(researchResult("report", result, p.provider.Name(), p.warning))
	}
}

// --- researchguy_review ---

func reviewTool() mcp.Tool {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Create a literature review / synthesis on a topic. Optionally provide source file paths."),
		mcp.WithString("topic", mcp.Required(), mcp.Description("The topic to review")),
		mcp.WithString("sources", mcp.Description("Comma-separated file paths to include as source material")),
		mcp.WithBoolean("no_research", mcp.Description("Skip searching existing research for context")),
		mcp.WithString("max_age", mcp.Description("Max age for research freshness filter (e.g. 90d, 2w, 24h, or none)")),
	}
	return mcp.NewTool("researchguy_review", append(opts, hybridOptions()...)...)
}

func reviewHandler(cfg *config.Config, provider llm.Provider) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		topic, err := req.RequireString("topic")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		p, err := parseResearchParams(cfg, provider, req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		runner := research.NewRunner(cfg, p.provider)
		result, err := runner.Run(ctx, research.Task{
			Type:        research.TypeReview,
			Topic:       topic,
			Sources:     splitList(req.GetString("sources", "")),
			Quiet:       true,
			NoResearch:  req.GetBool("no_research", false),
			MaxAge:      req.GetString("max_age", ""),
			Mode:        p.mode,
			BranchCount: p.branches,
			Projects:    p.projects,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("review failed: %v", err)), nil
		}

		return toolResultJSON(researchResult("review", result, p.provider.Name(), p.warning))
	}
}

// --- researchguy_compare ---

func compareTool() mcp.Tool {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Side-by-side comparative analysis of two topics or two existing documents."),
		mcp.WithString("subject1", mcp.Required(), mcp.Description("First subject/topic to compare")),
		mcp.WithString("subject2", mcp.Required(), mcp.Description("Second subject/topic to compare")),
		mcp.WithString("sources", mcp.Description("Comma-separated paths to two existing documents to compare instead of topics")),
		mcp.WithBoolean("no_research", mcp.Description("Skip searching existing research for context")),
		mcp.WithString("max_age", mcp.Description("Max age for research freshness filter (e.g. 90d, 2w, 24h, or none)")),
	}
	return mcp.NewTool("researchguy_compare", append(opts, hybridOptions()...)...)
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
		p, err := parseResearchParams(cfg, provider, req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		runner := research.NewRunner(cfg, p.provider)
		result, err := runner.Run(ctx, research.Task{
			Type:        research.TypeCompare,
			Topic:       subject1 + " vs " + subject2,
			Sources:     splitList(req.GetString("sources", "")),
			Quiet:       true,
			NoResearch:  req.GetBool("no_research", false),
			MaxAge:      req.GetString("max_age", ""),
			Mode:        p.mode,
			BranchCount: p.branches,
			Projects:    p.projects,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("compare failed: %v", err)), nil
		}

		return toolResultJSON(researchResult("comparison", result, p.provider.Name(), p.warning))
	}
}

// splitList splits a comma-separated list, trimming blanks.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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
		mcp.WithDescription("Semantic search across research documents via grepai. Returns ranked results; each carries its grepai project and on-disk path."),
		mcp.WithString("query", mcp.Required(), mcp.Description("The search query")),
		mcp.WithNumber("limit", mcp.Description("Max results to return (default 10)")),
		mcp.WithString("max_age", mcp.Description("Filter results by freshness (e.g. 90d, 2w, 24h)")),
		projectsOption(),
	)
}

func searchHandler(cfg *config.Config) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		limit := req.GetInt("limit", 10)

		results, err := search.QueryJSONProjects(cfg, query, limit, req.GetStringSlice("projects", nil))
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

func contextTool() mcp.Tool {
	return mcp.NewTool("researchguy_context",
		mcp.WithDescription("Get pre-formatted research context for a topic. Returns relevant excerpts with source files, grepai project, and freshness metadata. Does not trigger an LLM call; purely retrieves and formats existing research. Large files contribute only their matched chunks."),
		mcp.WithString("topic", mcp.Required(), mcp.Description("The topic to gather context for")),
		mcp.WithString("max_age", mcp.Description("Max age for freshness filter (e.g. 90d, 2w, 24h, or none to include older research such as past book research). Default: from config or 90d")),
		mcp.WithNumber("limit", mcp.Description("Max search results to include (default 10)")),
		projectsOption(),
	)
}

func contextHandler(cfg *config.Config) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		topic, err := req.RequireString("topic")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		result, err := search.BuildContext(cfg, topic, search.ContextOptions{
			Limit:    req.GetInt("limit", 10),
			MaxAge:   req.GetString("max_age", ""),
			Projects: req.GetStringSlice("projects", nil),
		})
		if err != nil {
			if strings.HasPrefix(err.Error(), "invalid max_age") {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
		}
		return toolResultJSON(result)
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

// maxReadBytes caps a researchguy_read response so one large file (combined
// book research runs to hundreds of KB) can't flood the caller's context.
var maxReadBytes = 100_000

func readTool() mcp.Tool {
	return mcp.NewTool("researchguy_read",
		mcp.WithDescription("Read a research document. Accepts a path relative to the research directory (e.g. 'llm/agents.md'), a file_path from researchguy_search/context results (e.g. 'sentinel-personal/the_poisoned_well/research/web/001-researcher-1.md'), or an absolute path inside a configured read root. Output is capped at 100KB; use start_line/end_line (search hits carry them) to read part of a large file."),
		mcp.WithString("path", mcp.Required(), mcp.Description("Path to the file (e.g. llm/agents.md)")),
		mcp.WithNumber("start_line", mcp.Description("First line to return, 1-based (default 1)")),
		mcp.WithNumber("end_line", mcp.Description("Last line to return, inclusive (default: end of file)")),
	)
}

func readHandler(cfg *config.Config) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p, err := req.RequireString("path")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		absFile, err := resolveReadPath(cfg, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, err := os.ReadFile(absFile)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("reading file: %v", err)), nil
		}
		text := string(data)

		start, end := req.GetInt("start_line", 0), req.GetInt("end_line", 0)
		if start > 0 || end > 0 {
			lines := strings.Split(text, "\n")
			if start < 1 {
				start = 1
			}
			if end <= 0 || end > len(lines) {
				end = len(lines)
			}
			if start > len(lines) || start > end {
				return mcp.NewToolResultError(fmt.Sprintf("line range %d-%d is outside the file (%d lines)", start, end, len(lines))), nil
			}
			text = strings.Join(lines[start-1:end], "\n")
		}

		if len(text) > maxReadBytes {
			total := len(text)
			text = search.TruncateBytes(text, maxReadBytes) +
				fmt.Sprintf("\n\n[truncated: %d of %d bytes shown; pass start_line/end_line to read more]", maxReadBytes, total)
		}
		return mcp.NewToolResultText(text), nil
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
