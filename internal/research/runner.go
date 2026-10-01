package research

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/search"
	"github.com/marklubin/researchguy/internal/tools"
)

// Runner orchestrates research tasks.
type Runner struct {
	cfg             *config.Config
	provider        llm.Provider
	utilityProvider llm.Provider
}

func NewRunner(cfg *config.Config, provider llm.Provider) *Runner {
	utilityProvider := provider
	if p, err := llm.NewUtilityProvider(cfg); err == nil && p != nil {
		utilityProvider = p
	}
	return NewRunnerWithUtility(cfg, provider, utilityProvider)
}

// NewRunnerWithUtility constructs a runner with an explicit provider for
// bounded utility calls. It primarily exists to keep categorization isolated
// from an expensive hybrid research provider and to make that routing
// directly testable.
func NewRunnerWithUtility(cfg *config.Config, provider, utilityProvider llm.Provider) *Runner {
	if utilityProvider == nil {
		utilityProvider = provider
	}
	return &Runner{cfg: cfg, provider: provider, utilityProvider: utilityProvider}
}

// Run executes a research task and returns the result.
func (r *Runner) Run(ctx context.Context, task Task) (RunResult, error) {
	switch task.Type {
	case TypeAsk:
		return r.runAsk(ctx, task)
	case TypeDive:
		return r.runDive(ctx, task)
	case TypeWatch:
		return r.runWatch(ctx, task)
	case TypeReview:
		return r.runReview(ctx, task)
	case TypeEnrich:
		return r.runEnrich(ctx, task)
	case TypeCompare:
		return r.runCompare(ctx, task)
	default:
		return RunResult{}, fmt.Errorf("unknown task type: %q", task.Type)
	}
}

func (r *Runner) defaultTools() []tools.Tool {
	if r.cfg.Tools.Enabled {
		return tools.DefaultTools()
	}
	return nil
}

func (r *Runner) runAsk(ctx context.Context, task Task) (RunResult, error) {
	// Gather research context unless --no-research
	researchContext := ""
	if !task.NoResearch {
		researchContext = r.gatherResearchContext(task)
	}

	// Build system prompt (with or without research context)
	sysPrompt := AskSystemPrompt(researchContext)

	resp, err := r.provider.Complete(ctx, llm.Request{
		SystemPrompt: sysPrompt,
		UserPrompt:   task.Topic,
		Tools:        r.defaultTools(),
		Mode:         task.Mode,
		BranchCount:  task.BranchCount,
	})
	if err != nil {
		return RunResult{}, err
	}
	metadata := r.providerMetadata()

	if !task.Quiet {
		fmt.Println(resp)
	}

	// Save unless --no-save
	if task.NoSave {
		return RunResult{Response: resp, Metadata: metadata}, nil
	}

	outPath, err := r.outputPath(ctx, task, "-ask")
	if err != nil {
		return RunResult{Response: resp, Metadata: metadata}, err
	}
	header := fmt.Sprintf("# Ask: %s\n\n*Generated: %s | Backend: %s*\n\n---\n\n",
		task.Topic, time.Now().Format("2006-01-02 15:04"), r.provider.Name())

	if err := os.WriteFile(outPath, []byte(header+resp), 0644); err != nil {
		return RunResult{Response: resp, Metadata: metadata}, fmt.Errorf("writing output: %w", err)
	}

	return RunResult{FilePath: outPath, Response: resp, Metadata: metadata}, nil
}

// gatherResearchContext searches existing research via grepai, filters by freshness,
// reads contents, and returns a formatted context string.
// Failures are non-fatal: warnings go to stderr, empty string returned on error.
func (r *Runner) gatherResearchContext(task Task) string {
	// Determine max age: task override > config default
	maxAgeStr := r.cfg.Ask.MaxAge
	if task.MaxAge != "" {
		maxAgeStr = task.MaxAge
	}
	if _, err := search.ParseMaxAge(maxAgeStr); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: invalid max-age %q, using 90d default: %v\n", maxAgeStr, err)
		maxAgeStr = "90d"
	}

	result, err := search.BuildContext(r.cfg, task.Topic, search.ContextOptions{
		Limit:    10,
		MaxAge:   maxAgeStr,
		Projects: task.Projects,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: research search failed (continuing without context): %v\n", err)
		return ""
	}
	return result.Context
}

func (r *Runner) runDive(ctx context.Context, task Task) (RunResult, error) {
	outPath, err := r.outputPath(ctx, task, "")
	if err != nil {
		return RunResult{}, err
	}

	prompt := fmt.Sprintf("Produce a comprehensive deep-dive research report on: %s", task.Topic)

	researchContext := ""
	if !task.NoResearch {
		researchContext = r.gatherResearchContext(task)
	}

	resp, err := r.provider.Complete(ctx, llm.Request{
		SystemPrompt: SystemPrompt(TypeDive, researchContext),
		UserPrompt:   prompt,
		MaxTokens:    r.cfg.Claude.MaxTokens,
		Tools:        r.defaultTools(),
		Mode:         task.Mode,
		BranchCount:  task.BranchCount,
	})
	if err != nil {
		return RunResult{}, err
	}
	metadata := r.providerMetadata()

	header := fmt.Sprintf("# %s\n\n*Generated: %s | Backend: %s*\n\n---\n\n",
		task.Topic, time.Now().Format("2006-01-02 15:04"), r.provider.Name())

	if err := os.WriteFile(outPath, []byte(header+resp), 0644); err != nil {
		return RunResult{Response: resp, Metadata: metadata}, fmt.Errorf("writing output: %w", err)
	}

	return RunResult{FilePath: outPath, Response: resp, Metadata: metadata}, nil
}

func (r *Runner) runWatch(ctx context.Context, task Task) (RunResult, error) {
	outPath, err := r.outputPath(ctx, task, "-watch")
	if err != nil {
		return RunResult{}, err
	}

	prompt := fmt.Sprintf("Report on the latest developments regarding: %s", task.Topic)

	resp, err := r.provider.Complete(ctx, llm.Request{
		SystemPrompt: SystemPrompt(TypeWatch, ""),
		UserPrompt:   prompt,
		MaxTokens:    r.cfg.Claude.MaxTokens,
		Tools:        r.defaultTools(),
		Mode:         task.Mode,
		BranchCount:  task.BranchCount,
	})
	if err != nil {
		return RunResult{}, err
	}
	metadata := r.providerMetadata()

	entry := fmt.Sprintf("\n\n---\n\n## Update: %s\n\n*Backend: %s*\n\n%s\n",
		time.Now().Format("2006-01-02 15:04"), r.provider.Name(), resp)

	// Append to existing file or create new
	f, err := os.OpenFile(outPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return RunResult{}, fmt.Errorf("opening updates file: %w", err)
	}
	defer f.Close()

	if info, _ := f.Stat(); info.Size() == 0 {
		header := fmt.Sprintf("# %s — Watch Updates\n", task.Topic)
		f.WriteString(header)
	}
	f.WriteString(entry)

	return RunResult{FilePath: outPath, Response: resp, Metadata: metadata}, nil
}

func (r *Runner) runReview(ctx context.Context, task Task) (RunResult, error) {
	outPath, err := r.outputPath(ctx, task, "-review")
	if err != nil {
		return RunResult{}, err
	}

	var promptBuilder strings.Builder
	promptBuilder.WriteString(fmt.Sprintf("Create a literature review / synthesis on: %s\n", task.Topic))

	// Read source files if provided
	for _, src := range task.Sources {
		content, err := os.ReadFile(src)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not read source %q: %v\n", src, err)
			continue
		}
		promptBuilder.WriteString(fmt.Sprintf("\n\n--- Source: %s ---\n%s\n", filepath.Base(src), string(content)))
	}

	researchContext := ""
	if !task.NoResearch {
		researchContext = r.gatherResearchContext(task)
	}

	resp, err := r.provider.Complete(ctx, llm.Request{
		SystemPrompt: SystemPrompt(TypeReview, researchContext),
		UserPrompt:   promptBuilder.String(),
		MaxTokens:    r.cfg.Claude.MaxTokens,
		Tools:        r.defaultTools(),
		Mode:         task.Mode,
		BranchCount:  task.BranchCount,
	})
	if err != nil {
		return RunResult{}, err
	}
	metadata := r.providerMetadata()

	header := fmt.Sprintf("# Review: %s\n\n*Generated: %s | Backend: %s*\n\n---\n\n",
		task.Topic, time.Now().Format("2006-01-02 15:04"), r.provider.Name())

	if err := os.WriteFile(outPath, []byte(header+resp), 0644); err != nil {
		return RunResult{Response: resp, Metadata: metadata}, fmt.Errorf("writing output: %w", err)
	}

	return RunResult{FilePath: outPath, Response: resp, Metadata: metadata}, nil
}

func (r *Runner) runEnrich(ctx context.Context, task Task) (RunResult, error) {
	if len(task.Sources) == 0 {
		return RunResult{}, fmt.Errorf("enrich requires a source document path")
	}

	docPath := task.Sources[0]
	content, err := os.ReadFile(docPath)
	if err != nil {
		return RunResult{}, fmt.Errorf("reading document: %w", err)
	}

	prompt := fmt.Sprintf("Enrich and expand the following research document:\n\n%s", string(content))

	resp, err := r.provider.Complete(ctx, llm.Request{
		SystemPrompt: SystemPrompt(TypeEnrich, ""),
		UserPrompt:   prompt,
		MaxTokens:    r.cfg.Claude.MaxTokens,
		Tools:        r.defaultTools(),
		Mode:         task.Mode,
		BranchCount:  task.BranchCount,
	})
	if err != nil {
		return RunResult{}, err
	}
	metadata := r.providerMetadata()

	// Write enriched version alongside original
	dir := filepath.Dir(docPath)
	base := strings.TrimSuffix(filepath.Base(docPath), filepath.Ext(docPath))
	outPath := filepath.Join(dir, base+"-enriched.md")

	header := fmt.Sprintf("*Enriched: %s | Backend: %s | Source: %s*\n\n---\n\n",
		time.Now().Format("2006-01-02 15:04"), r.provider.Name(), filepath.Base(docPath))

	if err := os.WriteFile(outPath, []byte(header+resp), 0644); err != nil {
		return RunResult{Response: resp, Metadata: metadata}, fmt.Errorf("writing enriched output: %w", err)
	}

	return RunResult{FilePath: outPath, Response: resp, Metadata: metadata}, nil
}

func (r *Runner) runCompare(ctx context.Context, task Task) (RunResult, error) {
	outPath, err := r.outputPath(ctx, task, "-comparison")
	if err != nil {
		return RunResult{}, err
	}

	var promptBuilder strings.Builder

	if len(task.Sources) >= 2 {
		promptBuilder.WriteString("Compare the following two documents:\n")
		for i, src := range task.Sources[:2] {
			content, err := os.ReadFile(src)
			if err != nil {
				return RunResult{}, fmt.Errorf("reading source %d (%s): %w", i+1, src, err)
			}
			promptBuilder.WriteString(fmt.Sprintf("\n\n--- Document %d: %s ---\n%s\n", i+1, filepath.Base(src), string(content)))
		}
	} else {
		promptBuilder.WriteString(fmt.Sprintf("Produce a comprehensive comparative analysis of: %s", task.Topic))
	}

	// In document-comparison mode (--sources) task.Topic is a placeholder
	// ("document comparison"), not a real query, so skip the context search.
	researchContext := ""
	if !task.NoResearch && len(task.Sources) < 2 {
		researchContext = r.gatherResearchContext(task)
	}

	resp, err := r.provider.Complete(ctx, llm.Request{
		SystemPrompt: SystemPrompt(TypeCompare, researchContext),
		UserPrompt:   promptBuilder.String(),
		MaxTokens:    r.cfg.Claude.MaxTokens,
		Tools:        r.defaultTools(),
		Mode:         task.Mode,
		BranchCount:  task.BranchCount,
	})
	if err != nil {
		return RunResult{}, err
	}
	metadata := r.providerMetadata()

	header := fmt.Sprintf("# Comparison: %s\n\n*Generated: %s | Backend: %s*\n\n---\n\n",
		task.Topic, time.Now().Format("2006-01-02 15:04"), r.provider.Name())

	if err := os.WriteFile(outPath, []byte(header+resp), 0644); err != nil {
		return RunResult{Response: resp, Metadata: metadata}, fmt.Errorf("writing output: %w", err)
	}

	return RunResult{FilePath: outPath, Response: resp, Metadata: metadata}, nil
}

func (r *Runner) providerMetadata() string {
	if p, ok := r.provider.(llm.MetadataProvider); ok {
		return p.Metadata()
	}
	return ""
}

// outputPath returns where a task's report is written: task.OutPath when set
// (no categorizer call), otherwise a categorized path under research_dir with
// suffix added before ".md". The parent directory is created.
func (r *Runner) outputPath(ctx context.Context, task Task, suffix string) (string, error) {
	if task.OutPath != "" {
		p := config.ExpandPath(task.OutPath)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			return "", fmt.Errorf("creating output dir: %w", err)
		}
		return p, nil
	}
	dir, filename, err := r.categorizedPath(ctx, task.Topic)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating project dir: %w", err)
	}
	return filepath.Join(dir, strings.TrimSuffix(filename, ".md")+suffix+".md"), nil
}

// categorizedPath uses the LLM to determine the category directory and filename
// for a research topic. Falls back to uncategorized/slugified on error.
func (r *Runner) categorizedPath(ctx context.Context, topic string) (dir string, filename string, err error) {
	researchDir := config.ExpandPath(r.cfg.ResearchDir)
	cats, _ := ExistingCategories(researchDir)
	loc, err := Categorize(ctx, r.utilityProvider, topic, cats)
	if err != nil {
		loc = FileLocation{Category: "uncategorized", Filename: Slugify(topic)}
	}
	dir = filepath.Join(researchDir, loc.Category)
	filename = UniqueFilename(dir, loc.Filename) + ".md"
	return dir, filename, nil
}
