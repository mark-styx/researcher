package mcpserver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/research"
	"github.com/marklubin/researchguy/internal/search"
)

// newProvider builds a per-call provider when a tool call names a backend.
// Tests replace it.
var newProvider = llm.NewProvider

func projectsOption() mcp.ToolOption {
	return mcp.WithArray("projects",
		mcp.Description("grepai workspace projects to search for existing research (e.g. [\"research\", \"the_poisoned_well\"]). Default: grepai.projects from config"),
		mcp.WithStringItems(),
	)
}

// hybridOptions are shared by dive, review, and compare.
func hybridOptions() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithString("backend", mcp.Description("Backend for this call. mode and branches only take effect with hybrid. Default: the server's default backend"), mcp.Enum("claude", "ollama", "hybrid")),
		mcp.WithString("mode", mcp.Description("Hybrid branch-role set: landscape (tools/alternatives) or inquiry (contested claims, adds counter-evidence and funding-provenance branches)"), mcp.Enum("landscape", "inquiry")),
		mcp.WithNumber("branches", mcp.Description("Hybrid fan-out: number of angles investigated in parallel (0 = backend default)")),
		projectsOption(),
	}
}

// researchParams holds the per-call options common to research tools.
type researchParams struct {
	provider llm.Provider
	mode     string
	branches int
	projects []string
	warning  string
}

// parseResearchParams validates mode/branches/backend/projects and picks the
// provider for the call.
func parseResearchParams(cfg *config.Config, defaultProvider llm.Provider, req mcp.CallToolRequest) (researchParams, error) {
	p := researchParams{
		provider: defaultProvider,
		mode:     req.GetString("mode", ""),
		branches: req.GetInt("branches", 0),
		projects: req.GetStringSlice("projects", nil),
	}
	if !llm.IsValidMode(p.mode) {
		return p, fmt.Errorf("invalid mode %q (want landscape or inquiry)", p.mode)
	}
	if p.branches < 0 {
		return p, fmt.Errorf("branches must be >= 0, got %d", p.branches)
	}
	if backend := req.GetString("backend", ""); backend != "" {
		switch backend {
		case "claude", "ollama", "hybrid":
		default:
			return p, fmt.Errorf("invalid backend %q (want claude, ollama, or hybrid)", backend)
		}
		prov, err := newProvider(cfg, backend, "")
		if err != nil {
			return p, fmt.Errorf("creating %s provider: %w", backend, err)
		}
		p.provider = prov
	}
	if (p.mode != "" || p.branches > 0) && p.provider.Name() != "hybrid" {
		p.warning = fmt.Sprintf("mode/branches ignored: backend is %s, not hybrid", p.provider.Name())
	}
	return p, nil
}

// researchResult builds the JSON body shared by research tools.
func researchResult(key string, r research.RunResult, backend, warning string) map[string]any {
	out := r.Fields(key, backend)
	if warning != "" {
		out["warning"] = warning
	}
	return out
}

// resolveReadPath maps a researchguy_read path to a real file inside
// research_dir or a configured read root. It accepts paths relative to
// research_dir, grepai workspace hit paths ("<workspace>/<project>/<rel>"),
// and absolute paths. Symlinks are resolved before the containment check.
func resolveReadPath(cfg *config.Config, p string) (string, error) {
	researchDir := config.ExpandPath(cfg.ResearchDir)
	var candidate string
	if filepath.IsAbs(p) {
		candidate = filepath.Clean(p)
	} else {
		cleaned := filepath.Clean(p)
		if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
			return "", fmt.Errorf("path must be relative to the research directory")
		}
		slashed := filepath.ToSlash(cleaned)
		if ws := cfg.Grepai.Workspace; ws != "" && strings.HasPrefix(slashed, ws+"/") {
			_, abs := search.NewResolver(cfg).Resolve(slashed)
			if abs == "" {
				return "", fmt.Errorf("unknown grepai project in %q", p)
			}
			candidate = abs
		} else {
			candidate = filepath.Join(researchDir, cleaned)
		}
	}

	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolving file path: %w", err)
	}

	roots := []string{researchDir}
	for _, r := range cfg.ReadRoots {
		roots = append(roots, config.ExpandPath(r))
	}
	for _, root := range roots {
		rr, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		rr, err = filepath.Abs(rr)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(rr, resolved)
		if err != nil {
			continue
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("path must be within the research directory or a configured read root")
}
