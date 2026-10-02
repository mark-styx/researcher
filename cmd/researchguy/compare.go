package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/research"
	"github.com/spf13/cobra"
)

func compareCmd() *cobra.Command {
	var backend, model, sources, maxAge, mode string
	var noResearch, asJSON bool
	var outPath string
	var projects []string
	var branches int

	cmd := &cobra.Command{
		Use:   "compare <topicA> <topicB>",
		Short: "Side-by-side comparative analysis of two topics or documents",
		Long: `Generate a structured comparative analysis. Supports two modes:

Topic comparison: provide two topic arguments to research and compare them.
Document comparison: use --sources to compare two existing research documents.`,
		Example: `  researchguy compare "React" "Vue"
  researchguy compare "Python" "Go" --backend ollama
  researchguy compare --sources llm/agents.md,llm/tools.md`,
		GroupID: "research",
		Args:    cobra.RangeArgs(0, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var sourceFiles []string
			if sources != "" {
				sourceFiles = strings.Split(sources, ",")
			}

			if len(args) == 0 && len(sourceFiles) < 2 {
				return fmt.Errorf("provide two topics as arguments, or --sources with two file paths")
			}
			if len(args) == 1 {
				return fmt.Errorf("compare requires exactly two topics (got 1)")
			}

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			provider, err := llm.NewProvider(cfg, backend, model)
			if err != nil {
				return fmt.Errorf("creating LLM provider: %w", err)
			}

			topic := "document comparison"
			if len(args) == 2 {
				topic = args[0] + " vs " + args[1]
			}

			runner := research.NewRunner(cfg, provider)
			result, err := runner.Run(context.Background(), research.Task{
				Type:        research.TypeCompare,
				Topic:       topic,
				Sources:     sourceFiles,
				NoResearch:  noResearch,
				MaxAge:      maxAge,
				Mode:        mode,
				BranchCount: branches,
				OutPath:     outPath,
				Projects:    projects,
			})
			if err != nil {
				return fmt.Errorf("compare failed: %w", err)
			}

			if asJSON {
				return printJSON(result.Fields("comparison", provider.Name()))
			}
			fmt.Printf("Comparison saved to: %s\n", result.FilePath)
			printRunRecord(cmd.OutOrStdout(), result.RunDir)
			return nil
		},
	}

	cmd.Flags().StringVar(&backend, "backend", "", "LLM backend (claude, ollama, codex, hybrid)")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	cmd.Flags().StringVar(&sources, "sources", "", "Comma-separated paths to two documents to compare")
	cmd.Flags().BoolVar(&noResearch, "no-research", false, "Skip searching existing research for context")
	cmd.Flags().StringVar(&maxAge, "max-age", "", "Max age for research freshness filter (e.g. 90d, 2w, 24h, none)")
	cmd.Flags().StringVar(&mode, "mode", "", "Epistemic branch set for the hybrid backend (landscape, inquiry)")
	cmd.Flags().IntVar(&branches, "branches", 0, "Number of angles to investigate in parallel (hybrid backend only; 0 = backend default)")
	cmd.Flags().StringVar(&outPath, "out", "", "Write the report to this exact path (skips the LLM categorizer)")
	cmd.Flags().StringSliceVar(&projects, "project", nil, "grepai workspace project to search for context (repeatable; default: grepai.projects)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print JSON {comparison, saved_to, backend, metadata, run_id, run_dir} instead of a status line")
	return cmd
}
