package research

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marklubin/researcher/internal/config"
	"github.com/marklubin/researcher/internal/llm"
	"github.com/marklubin/researcher/internal/tools"
)

// Runner orchestrates research tasks.
type Runner struct {
	cfg      *config.Config
	provider llm.Provider
}

func NewRunner(cfg *config.Config, provider llm.Provider) *Runner {
	return &Runner{cfg: cfg, provider: provider}
}

// Run executes a research task and returns the output file path.
func (r *Runner) Run(ctx context.Context, task Task) (string, error) {
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
	default:
		return "", fmt.Errorf("unknown task type: %q", task.Type)
	}
}

func (r *Runner) defaultTools() []tools.Tool {
	if r.cfg.Tools.Enabled {
		return tools.DefaultTools()
	}
	return nil
}

func (r *Runner) runAsk(ctx context.Context, task Task) (string, error) {
	resp, err := r.provider.Complete(ctx, llm.Request{
		SystemPrompt: SystemPrompt(TypeAsk),
		UserPrompt:   task.Topic,
		Tools:        r.defaultTools(),
	})
	if err != nil {
		return "", err
	}
	fmt.Println(resp)
	return "", nil
}

func (r *Runner) runDive(ctx context.Context, task Task) (string, error) {
	dir, filename, err := r.categorizedPath(ctx, task.Topic)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating project dir: %w", err)
	}

	prompt := fmt.Sprintf("Produce a comprehensive deep-dive research report on: %s", task.Topic)

	resp, err := r.provider.Complete(ctx, llm.Request{
		SystemPrompt: SystemPrompt(TypeDive),
		UserPrompt:   prompt,
		MaxTokens:    r.cfg.Claude.MaxTokens,
		Tools:        r.defaultTools(),
	})
	if err != nil {
		return "", err
	}

	outPath := filepath.Join(dir, filename)
	header := fmt.Sprintf("# %s\n\n*Generated: %s | Backend: %s*\n\n---\n\n",
		task.Topic, time.Now().Format("2006-01-02 15:04"), r.provider.Name())

	if err := os.WriteFile(outPath, []byte(header+resp), 0644); err != nil {
		return "", fmt.Errorf("writing output: %w", err)
	}

	return outPath, nil
}

func (r *Runner) runWatch(ctx context.Context, task Task) (string, error) {
	dir, filename, err := r.categorizedPath(ctx, task.Topic)
	if err != nil {
		return "", err
	}
	// Add -watch suffix to filename
	filename = strings.TrimSuffix(filename, ".md") + "-watch.md"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating project dir: %w", err)
	}

	prompt := fmt.Sprintf("Report on the latest developments regarding: %s", task.Topic)

	resp, err := r.provider.Complete(ctx, llm.Request{
		SystemPrompt: SystemPrompt(TypeWatch),
		UserPrompt:   prompt,
		MaxTokens:    r.cfg.Claude.MaxTokens,
		Tools:        r.defaultTools(),
	})
	if err != nil {
		return "", err
	}

	outPath := filepath.Join(dir, filename)
	entry := fmt.Sprintf("\n\n---\n\n## Update: %s\n\n*Backend: %s*\n\n%s\n",
		time.Now().Format("2006-01-02 15:04"), r.provider.Name(), resp)

	// Append to existing file or create new
	f, err := os.OpenFile(outPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return "", fmt.Errorf("opening updates file: %w", err)
	}
	defer f.Close()

	if info, _ := f.Stat(); info.Size() == 0 {
		header := fmt.Sprintf("# %s — Watch Updates\n", task.Topic)
		f.WriteString(header)
	}
	f.WriteString(entry)

	return outPath, nil
}

func (r *Runner) runReview(ctx context.Context, task Task) (string, error) {
	dir, filename, err := r.categorizedPath(ctx, task.Topic)
	if err != nil {
		return "", err
	}
	// Add -review suffix to filename
	filename = strings.TrimSuffix(filename, ".md") + "-review.md"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating project dir: %w", err)
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

	resp, err := r.provider.Complete(ctx, llm.Request{
		SystemPrompt: SystemPrompt(TypeReview),
		UserPrompt:   promptBuilder.String(),
		MaxTokens:    r.cfg.Claude.MaxTokens,
		Tools:        r.defaultTools(),
	})
	if err != nil {
		return "", err
	}

	outPath := filepath.Join(dir, filename)
	header := fmt.Sprintf("# Review: %s\n\n*Generated: %s | Backend: %s*\n\n---\n\n",
		task.Topic, time.Now().Format("2006-01-02 15:04"), r.provider.Name())

	if err := os.WriteFile(outPath, []byte(header+resp), 0644); err != nil {
		return "", fmt.Errorf("writing output: %w", err)
	}

	return outPath, nil
}

func (r *Runner) runEnrich(ctx context.Context, task Task) (string, error) {
	if len(task.Sources) == 0 {
		return "", fmt.Errorf("enrich requires a source document path")
	}

	docPath := task.Sources[0]
	content, err := os.ReadFile(docPath)
	if err != nil {
		return "", fmt.Errorf("reading document: %w", err)
	}

	prompt := fmt.Sprintf("Enrich and expand the following research document:\n\n%s", string(content))

	resp, err := r.provider.Complete(ctx, llm.Request{
		SystemPrompt: SystemPrompt(TypeEnrich),
		UserPrompt:   prompt,
		MaxTokens:    r.cfg.Claude.MaxTokens,
		Tools:        r.defaultTools(),
	})
	if err != nil {
		return "", err
	}

	// Write enriched version alongside original
	dir := filepath.Dir(docPath)
	base := strings.TrimSuffix(filepath.Base(docPath), filepath.Ext(docPath))
	outPath := filepath.Join(dir, base+"-enriched.md")

	header := fmt.Sprintf("*Enriched: %s | Backend: %s | Source: %s*\n\n---\n\n",
		time.Now().Format("2006-01-02 15:04"), r.provider.Name(), filepath.Base(docPath))

	if err := os.WriteFile(outPath, []byte(header+resp), 0644); err != nil {
		return "", fmt.Errorf("writing enriched output: %w", err)
	}

	return outPath, nil
}

// categorizedPath uses the LLM to determine the category directory and filename
// for a research topic. Falls back to uncategorized/slugified on error.
func (r *Runner) categorizedPath(ctx context.Context, topic string) (dir string, filename string, err error) {
	researchDir := config.ExpandPath(r.cfg.ResearchDir)
	cats, _ := ExistingCategories(researchDir)
	loc, err := Categorize(ctx, r.provider, topic, cats)
	if err != nil {
		loc = FileLocation{Category: "uncategorized", Filename: Slugify(topic)}
	}
	dir = filepath.Join(researchDir, loc.Category)
	filename = UniqueFilename(dir, loc.Filename) + ".md"
	return dir, filename, nil
}
