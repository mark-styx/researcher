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

func reviewCmd() *cobra.Command {
	var backend, model, sources, maxAge, mode string
	var noResearch, asJSON bool
	var outPath string
	var projects []string
	var branches int

	cmd := &cobra.Command{
		Use:   "review <topic>",
		Short: "Literature review / synthesis on a topic",
		Long: `Synthesize a literature review on a topic. Optionally provide source files
to include as context for the review. Existing research on the topic is also
searched via grepai and given to the LLM as background, unless --no-research
is set. Output is saved as markdown in the research directory.`,
		Example: `  researchguy review "machine learning optimization"
  researchguy review "transformer architectures" --sources paper1.md,paper2.md`,
		GroupID: "research",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			topic := args[0]

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			provider, err := llm.NewProvider(cfg, backend, model)
			if err != nil {
				return fmt.Errorf("creating LLM provider: %w", err)
			}

			var sourceFiles []string
			if sources != "" {
				sourceFiles = strings.Split(sources, ",")
			}

			runner := research.NewRunner(cfg, provider)
			result, err := runner.Run(context.Background(), research.Task{
				Type:        research.TypeReview,
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
				return fmt.Errorf("review failed: %w", err)
			}

			if asJSON {
				return printJSON(result.Fields("review", provider.Name()))
			}
			fmt.Printf("Review saved to: %s\n", result.FilePath)
			return nil
		},
	}

	cmd.Flags().StringVar(&backend, "backend", "", "LLM backend (claude, ollama, codex, hybrid)")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	cmd.Flags().StringVar(&sources, "sources", "", "Comma-separated source files")
	cmd.Flags().BoolVar(&noResearch, "no-research", false, "Skip searching existing research for context")
	cmd.Flags().StringVar(&maxAge, "max-age", "", "Max age for research freshness filter (e.g. 90d, 2w, 24h, none)")
	cmd.Flags().StringVar(&mode, "mode", "", "Epistemic branch set for the hybrid backend (landscape, inquiry)")
	cmd.Flags().IntVar(&branches, "branches", 0, "Number of angles to investigate in parallel (hybrid backend only; 0 = backend default)")
	cmd.Flags().StringVar(&outPath, "out", "", "Write the report to this exact path (skips the LLM categorizer)")
	cmd.Flags().StringSliceVar(&projects, "project", nil, "grepai workspace project to search for context (repeatable; default: grepai.projects)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print JSON {review, saved_to, backend, metadata} instead of a status line")
	return cmd
}
