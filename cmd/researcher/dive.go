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
	var backend, model string

	cmd := &cobra.Command{
		Use:   "dive <topic>",
		Short: "Deep research on a topic, outputs structured markdown",
		Long: `Generate a comprehensive research report on any topic. The report includes
an executive summary, key concepts, current state of the art, major players,
challenges, future directions, and references. Output is saved as markdown
in the research directory.`,
		Example: `  researcher dive "quantum computing"
  researcher dive "CRISPR gene editing" --backend ollama --model llama3`,
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
				Type:  research.TypeDive,
				Topic: topic,
			})
			if err != nil {
				return fmt.Errorf("research failed: %w", err)
			}

			fmt.Printf("Research saved to: %s\n", result.FilePath)
			return nil
		},
	}

	cmd.Flags().StringVar(&backend, "backend", "", "LLM backend (claude, ollama)")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	return cmd
}
