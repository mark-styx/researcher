package main

import (
	"context"
	"fmt"

	"github.com/marklubin/researcher/internal/config"
	"github.com/marklubin/researcher/internal/llm"
	"github.com/marklubin/researcher/internal/research"
	"github.com/spf13/cobra"
)

func diveCmd() *cobra.Command {
	var backend, model, maxAge, mode string
	var noResearch bool
	var branches int

	cmd := &cobra.Command{
		Use:   "dive <topic>",
		Short: "Deep research on a topic, outputs structured markdown",
		Long: `Generate a comprehensive research report on any topic. The report includes
an executive summary, key concepts, current state of the art, major players,
challenges, future directions, and references. Output is saved as markdown
in the research directory.

By default, existing research on the topic is searched via grepai and given
to the LLM as background before it starts. Use --no-research to skip this,
or --max-age to control the freshness filter.`,
		Example: `  researcher dive "quantum computing"
  researcher dive "CRISPR gene editing" --backend ollama --model llama3
  researcher dive "topic already covered elsewhere" --no-research`,
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

			runner := research.NewRunner(cfg, provider)
			result, err := runner.Run(context.Background(), research.Task{
				Type:        research.TypeDive,
				Topic:       topic,
				NoResearch:  noResearch,
				MaxAge:      maxAge,
				Mode:        mode,
				BranchCount: branches,
			})
			if err != nil {
				return fmt.Errorf("research failed: %w", err)
			}

			fmt.Printf("Research saved to: %s\n", result.FilePath)
			return nil
		},
	}

	cmd.Flags().StringVar(&backend, "backend", "", "LLM backend (claude, ollama, hybrid)")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	cmd.Flags().BoolVar(&noResearch, "no-research", false, "Skip searching existing research for context")
	cmd.Flags().StringVar(&maxAge, "max-age", "", "Max age for research freshness filter (e.g. 90d, 2w, 24h)")
	cmd.Flags().StringVar(&mode, "mode", "", "Epistemic branch set for the hybrid backend (landscape, inquiry)")
	cmd.Flags().IntVar(&branches, "branches", 0, "Number of angles to investigate in parallel (hybrid backend only; 0 = backend default)")
	return cmd
}
