package mcpserver

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/critique"
	"github.com/marklubin/researchguy/internal/llm"
)

// --- researchguy_critique ---

func critiqueTool() mcp.Tool {
	return mcp.NewTool("researchguy_critique",
		mcp.WithDescription("Flag claims in a text that its evidence doesn't support. Runs two critics in flag mode (nothing is rewritten): groundedness lists factual claims the evidence does not support (not found in the evidence given, which is not the same as false); narrative lists claims stated as settled without a distinct piece of evidence. Evidence paths resolve like researchguy_read (research_dir, search file_paths, or read roots)."),
		mcp.WithString("text", mcp.Description("Text to critique. Give this or text_path.")),
		mcp.WithString("text_path", mcp.Description("File holding the text to critique, resolved like researchguy_read")),
		mcp.WithArray("evidence_paths", mcp.Required(),
			mcp.Description("Evidence files, resolved like researchguy_read, read in order up to max_evidence_chars"),
			mcp.WithStringItems()),
		mcp.WithNumber("max_evidence_chars", mcp.Description(fmt.Sprintf("Cap on evidence sent to the critics (default %d)", critique.DefaultMaxEvidenceChars))),
		mcp.WithString("backend", mcp.Description("Backend for this call. Default: the server's default backend"), mcp.Enum("claude", "ollama", "hybrid")),
	)
}

func critiqueHandler(cfg *config.Config, provider llm.Provider) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		text := req.GetString("text", "")
		if p := req.GetString("text_path", ""); p != "" {
			if text != "" {
				return mcp.NewToolResultError("give text or text_path, not both"), nil
			}
			abs, err := resolveReadPath(cfg, p)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("reading text: %v", err)), nil
			}
			text = string(data)
		}
		if strings.TrimSpace(text) == "" {
			return mcp.NewToolResultError("text or text_path is required"), nil
		}

		var paths []string
		for _, p := range req.GetStringSlice("evidence_paths", nil) {
			abs, err := resolveReadPath(cfg, p)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			paths = append(paths, abs)
		}
		loaded, err := critique.LoadEvidence(paths, req.GetInt("max_evidence_chars", 0))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if backend := req.GetString("backend", ""); backend != "" {
			switch backend {
			case "claude", "ollama", "hybrid":
			default:
				return mcp.NewToolResultError(fmt.Sprintf("invalid backend %q (want claude, ollama, or hybrid)", backend)), nil
			}
			if provider, err = newProvider(cfg, backend, ""); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("creating %s provider: %v", backend, err)), nil
			}
		}

		res, err := critique.Run(ctx, func(ctx context.Context, system, user string) (string, error) {
			return provider.Complete(ctx, llm.Request{SystemPrompt: system, UserPrompt: user})
		}, text, loaded.Evidence)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		labels := make([]string, 0, len(loaded.Evidence))
		for _, e := range loaded.Evidence {
			labels = append(labels, e.Label)
		}
		return toolResultJSON(map[string]any{
			"groundedness":   res.Groundedness,
			"narrative":      res.Narrative,
			"flags":          res.Flags,
			"evidence":       labels,
			"evidence_chars": loaded.Chars,
			"truncated":      loaded.Truncated,
			"skipped":        loaded.Skipped,
			"backend":        provider.Name(),
		})
	}
}
