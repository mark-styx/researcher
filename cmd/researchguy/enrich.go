package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/charmbracelet/huh"
	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/research"
	"github.com/spf13/cobra"
)

func enrichCmd() *cobra.Command {
	var backend, model string

	cmd := &cobra.Command{
		Use:   "enrich [path/to/doc.md]",
		Short: "Expand an existing research document",
		Long: `Expand thin sections and add missing context to an existing research document.
Reads the document, identifies areas that need more depth, and produces an
enriched version saved alongside the original.

When called without arguments, presents an interactive file browser to select
a document from the research directory.`,
		Example: `  researchguy enrich                                   # interactive file picker
  researchguy enrich ./research/quantum-computing/README.md
  researchguy enrich report.md --backend ollama`,
		GroupID: "research",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			var docPath string
			if len(args) == 1 {
				docPath = args[0]
			} else {
				docPath, err = selectResearchFile(cfg)
				if err != nil {
					return err
				}
			}

			provider, err := llm.NewProvider(cfg, backend, model)
			if err != nil {
				return fmt.Errorf("creating LLM provider: %w", err)
			}

			runner := research.NewRunner(cfg, provider)
			result, err := runner.Run(context.Background(), research.Task{
				Type:    research.TypeEnrich,
				Topic:   docPath,
				Sources: []string{docPath},
			})
			if err != nil {
				return fmt.Errorf("enrich failed: %w", err)
			}

			fmt.Printf("Enriched document: %s\n", result.FilePath)
			return nil
		},
	}

	cmd.Flags().StringVar(&backend, "backend", "", "LLM backend (claude, ollama, codex, hybrid)")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	return cmd
}

// selectResearchFile presents an interactive TUI to select a research file.
// Returns the full path to the selected file.
func selectResearchFile(cfg *config.Config) (string, error) {
	researchDir := config.ExpandPath(cfg.ResearchDir)

	cats, err := research.ListCategories(researchDir)
	if err != nil {
		return "", fmt.Errorf("listing categories: %w", err)
	}
	if len(cats) == 0 {
		return "", fmt.Errorf("no research categories found in %s", researchDir)
	}

	// Select category
	catOptions := make([]huh.Option[string], len(cats))
	for i, c := range cats {
		catOptions[i] = huh.NewOption(c, c)
	}

	var category string
	err = huh.NewSelect[string]().
		Title("Select a category").
		Options(catOptions...).
		Value(&category).
		Run()
	if err != nil {
		return "", fmt.Errorf("category selection: %w", err)
	}

	// Select file within category
	files, err := research.ListCategoryFiles(researchDir, category)
	if err != nil {
		return "", fmt.Errorf("listing files in %s: %w", category, err)
	}
	if len(files) == 0 {
		return "", fmt.Errorf("no research files in category %q", category)
	}

	fileOptions := make([]huh.Option[string], len(files))
	for i, f := range files {
		label := fmt.Sprintf("%s  (%s)", f.Name, f.Modified.Format("2006-01-02"))
		fileOptions[i] = huh.NewOption(label, f.Path)
	}

	var filePath string
	err = huh.NewSelect[string]().
		Title(fmt.Sprintf("Select a file from %s", category)).
		Options(fileOptions...).
		Value(&filePath).
		Run()
	if err != nil {
		return "", fmt.Errorf("file selection: %w", err)
	}

	// Show relative path for confirmation
	rel, _ := filepath.Rel(researchDir, filePath)
	fmt.Printf("Selected: %s\n", rel)

	return filePath, nil
}
