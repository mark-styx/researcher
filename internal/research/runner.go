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
	"github.com/marklubin/researchguy/internal/store"
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

// Run executes a research task and returns the result. Each task gets a run
// record in the store (store.dir) that holds what it collected, whether or
// not it succeeds.
func (r *Runner) Run(ctx context.Context, task Task) (RunResult, error) {
	var do func(context.Context, Task, *store.Run) (RunResult, error)
	switch task.Type {
	case TypeAsk:
		do = r.runAsk
	case TypeDive:
		do = r.runDive
	case TypeWatch:
		do = r.runWatch
	case TypeReview:
		do = r.runReview
	case TypeEnrich:
		do = r.runEnrich
	case TypeCompare:
		do = r.runCompare
	default:
		return RunResult{}, fmt.Errorf("unknown task type: %q", task.Type)
	}

	run := r.startRun(task)
	result, err := do(ctx, task, run)
	r.finishRun(run, result, err)
	result.RunID = run.ID()
	result.RunDir = run.Dir()
	return result, err
}

// startRun opens the task's run record. A store that can't be written is
// a warning, not a failed task: the report is still produced, and the
// warning says its evidence isn't being kept.
func (r *Runner) startRun(task Task) *store.Run {
	if r.cfg.Store.Dir == "" {
		return nil
	}
	s, err := store.Open(r.cfg.Store.Dir)
	if err == nil {
		var run *store.Run
		run, err = s.StartRun(store.RunRecord{
			Kind:        task.Type,
			Topic:       task.Topic,
			Backend:     r.provider.Name(),
			Mode:        task.Mode,
			BranchCount: task.BranchCount,
			Sources:     task.Sources,
		})
		if err == nil {
			return run
		}
	}
	fmt.Fprintf(os.Stderr, "Warning: no run record for this task, its evidence won't be kept: %v\n", err)
	return nil
}

// complete runs the provider with a Capture that writes each tool result to
// the run as it arrives. Afterwards it saves any result the provider didn't
// stream, straight away, before a later call on the same provider (the
// categorizer, when no utility model is set) replaces them. The hybrid
// backend sets its own Capture for each worker.
func (r *Runner) complete(ctx context.Context, run *store.Run, req llm.Request) (string, error) {
	backend := r.provider.Name()
	if run != nil && req.Capture == nil {
		req.Capture = func(e llm.EvidenceRecord) string {
			seqs, err := run.Append(store.Capture{Backend: backend, Call: e.Call, Label: e.Label, Content: e.Content})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: writing run record: %v\n", err)
			}
			if len(seqs) == 0 {
				return ""
			}
			return fmt.Sprintf("E%d", seqs[0])
		}
	}
	resp, err := r.provider.Complete(ctx, req)
	if ep, ok := r.provider.(llm.EvidenceProvider); ok && run != nil {
		var captures []store.Capture
		for _, e := range ep.Evidence() {
			if e.ID == "" {
				captures = append(captures, store.Capture{Backend: backend, Call: e.Call, Label: e.Label, Content: e.Content})
			}
		}
		if _, cerr := run.Append(captures...); cerr != nil {
			fmt.Fprintf(os.Stderr, "Warning: writing run record: %v\n", cerr)
		}
	}
	return resp, err
}

// finishRun closes the run record with how the task ended.
func (r *Runner) finishRun(run *store.Run, result RunResult, runErr error) {
	if run == nil {
		return
	}
	fin := store.Finish{Status: store.StatusSucceeded, ReportPath: result.FilePath, Metadata: result.Metadata}
	if runErr != nil {
		fin.Status = store.StatusFailed
		fin.Error = runErr.Error()
		if fin.Metadata == "" {
			fin.Metadata = r.providerMetadata()
		}
	}
	if err := run.Finish(fin); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: writing run record: %v\n", err)
	}
}

// runTag is the report header's run reference, so a report's [E<n>]
// citations can be resolved against <store.dir>/runs/<id>/captures.jsonl.
func runTag(run *store.Run) string {
	if run == nil {
		return ""
	}
	return " | Run: " + run.ID()
}

func (r *Runner) defaultTools() []tools.Tool {
	if r.cfg.Tools.Enabled {
		return tools.DefaultTools()
	}
	return nil
}

func (r *Runner) runAsk(ctx context.Context, task Task, run *store.Run) (RunResult, error) {
	// Gather research context unless --no-research
	researchContext := ""
	if !task.NoResearch {
		researchContext = r.gatherResearchContext(task)
	}

	// Build system prompt (with or without research context)
	sysPrompt := AskSystemPrompt(researchContext)

	resp, err := r.complete(ctx, run, llm.Request{
		SystemPrompt: sysPrompt,
		UserPrompt:   task.Topic,
		Tools:        r.defaultTools(),
		Mode:         task.Mode,
		BranchCount:  task.BranchCount,
		Run:          run,
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
	header := fmt.Sprintf("# Ask: %s\n\n*Generated: %s | Backend: %s%s*\n\n---\n\n",
		task.Topic, time.Now().Format("2006-01-02 15:04"), r.provider.Name(), runTag(run))

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

func (r *Runner) runDive(ctx context.Context, task Task, run *store.Run) (RunResult, error) {
	outPath, err := r.outputPath(ctx, task, "")
	if err != nil {
		return RunResult{}, err
	}

	prompt := fmt.Sprintf("Produce a comprehensive deep-dive research report on: %s", task.Topic)

	researchContext := ""
	if !task.NoResearch {
		researchContext = r.gatherResearchContext(task)
	}

	resp, err := r.complete(ctx, run, llm.Request{
		SystemPrompt: SystemPrompt(TypeDive, researchContext),
		UserPrompt:   prompt,
		MaxTokens:    r.cfg.Claude.MaxTokens,
		Tools:        r.defaultTools(),
		Mode:         task.Mode,
		BranchCount:  task.BranchCount,
		Run:          run,
	})
	if err != nil {
		return RunResult{}, err
	}
	metadata := r.providerMetadata()

	header := fmt.Sprintf("# %s\n\n*Generated: %s | Backend: %s%s*\n\n---\n\n",
		task.Topic, time.Now().Format("2006-01-02 15:04"), r.provider.Name(), runTag(run))

	if err := os.WriteFile(outPath, []byte(header+resp), 0644); err != nil {
		return RunResult{Response: resp, Metadata: metadata}, fmt.Errorf("writing output: %w", err)
	}

	return RunResult{FilePath: outPath, Response: resp, Metadata: metadata}, nil
}

func (r *Runner) runWatch(ctx context.Context, task Task, run *store.Run) (RunResult, error) {
	outPath, err := r.outputPath(ctx, task, "-watch")
	if err != nil {
		return RunResult{}, err
	}

	prompt := fmt.Sprintf("Report on the latest developments regarding: %s", task.Topic)

	resp, err := r.complete(ctx, run, llm.Request{
		SystemPrompt: SystemPrompt(TypeWatch, ""),
		UserPrompt:   prompt,
		MaxTokens:    r.cfg.Claude.MaxTokens,
		Tools:        r.defaultTools(),
		Mode:         task.Mode,
		BranchCount:  task.BranchCount,
		Run:          run,
	})
	if err != nil {
		return RunResult{}, err
	}
	metadata := r.providerMetadata()

	entry := fmt.Sprintf("\n\n---\n\n## Update: %s\n\n*Backend: %s%s*\n\n%s\n",
		time.Now().Format("2006-01-02 15:04"), r.provider.Name(), runTag(run), resp)

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

func (r *Runner) runReview(ctx context.Context, task Task, run *store.Run) (RunResult, error) {
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

	resp, err := r.complete(ctx, run, llm.Request{
		SystemPrompt: SystemPrompt(TypeReview, researchContext),
		UserPrompt:   promptBuilder.String(),
		MaxTokens:    r.cfg.Claude.MaxTokens,
		Tools:        r.defaultTools(),
		Mode:         task.Mode,
		BranchCount:  task.BranchCount,
		Run:          run,
	})
	if err != nil {
		return RunResult{}, err
	}
	metadata := r.providerMetadata()

	header := fmt.Sprintf("# Review: %s\n\n*Generated: %s | Backend: %s%s*\n\n---\n\n",
		task.Topic, time.Now().Format("2006-01-02 15:04"), r.provider.Name(), runTag(run))

	if err := os.WriteFile(outPath, []byte(header+resp), 0644); err != nil {
		return RunResult{Response: resp, Metadata: metadata}, fmt.Errorf("writing output: %w", err)
	}

	return RunResult{FilePath: outPath, Response: resp, Metadata: metadata}, nil
}

func (r *Runner) runEnrich(ctx context.Context, task Task, run *store.Run) (RunResult, error) {
	if len(task.Sources) == 0 {
		return RunResult{}, fmt.Errorf("enrich requires a source document path")
	}

	docPath := task.Sources[0]
	content, err := os.ReadFile(docPath)
	if err != nil {
		return RunResult{}, fmt.Errorf("reading document: %w", err)
	}

	prompt := fmt.Sprintf("Enrich and expand the following research document:\n\n%s", string(content))

	resp, err := r.complete(ctx, run, llm.Request{
		SystemPrompt: SystemPrompt(TypeEnrich, ""),
		UserPrompt:   prompt,
		MaxTokens:    r.cfg.Claude.MaxTokens,
		Tools:        r.defaultTools(),
		Mode:         task.Mode,
		BranchCount:  task.BranchCount,
		Run:          run,
	})
	if err != nil {
		return RunResult{}, err
	}
	metadata := r.providerMetadata()

	// Write enriched version alongside original
	dir := filepath.Dir(docPath)
	base := strings.TrimSuffix(filepath.Base(docPath), filepath.Ext(docPath))
	outPath := filepath.Join(dir, base+"-enriched.md")

	header := fmt.Sprintf("*Enriched: %s | Backend: %s | Source: %s%s*\n\n---\n\n",
		time.Now().Format("2006-01-02 15:04"), r.provider.Name(), filepath.Base(docPath), runTag(run))

	if err := os.WriteFile(outPath, []byte(header+resp), 0644); err != nil {
		return RunResult{Response: resp, Metadata: metadata}, fmt.Errorf("writing enriched output: %w", err)
	}

	return RunResult{FilePath: outPath, Response: resp, Metadata: metadata}, nil
}

func (r *Runner) runCompare(ctx context.Context, task Task, run *store.Run) (RunResult, error) {
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

	resp, err := r.complete(ctx, run, llm.Request{
		SystemPrompt: SystemPrompt(TypeCompare, researchContext),
		UserPrompt:   promptBuilder.String(),
		MaxTokens:    r.cfg.Claude.MaxTokens,
		Tools:        r.defaultTools(),
		Mode:         task.Mode,
		BranchCount:  task.BranchCount,
		Run:          run,
	})
	if err != nil {
		return RunResult{}, err
	}
	metadata := r.providerMetadata()

	header := fmt.Sprintf("# Comparison: %s\n\n*Generated: %s | Backend: %s%s*\n\n---\n\n",
		task.Topic, time.Now().Format("2006-01-02 15:04"), r.provider.Name(), runTag(run))

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
