package main

import (
	"context"
	"fmt"

	"github.com/marklubin/researcher/internal/config"
	"github.com/marklubin/researcher/internal/llm"
	"github.com/marklubin/researcher/internal/research"
	"github.com/spf13/cobra"
)

func enrichCmd() *cobra.Command {
	var backend, model string

	cmd := &cobra.Command{
		Use:   "enrich <path/to/doc.md>",
		Short: "Expand an existing research document",
		Long: `Expand thin sections and add missing context to an existing research document.
Reads the document, identifies areas that need more depth, and produces an
enriched version saved alongside the original.`,
		Example: `  researcher enrich ./research/quantum-computing/README.md
  researcher enrich report.md --backend ollama`,
		GroupID: "research",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			docPath := args[0]

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			provider, err := llm.NewProvider(cfg, backend, model)
			if err != nil {
				return fmt.Errorf("creating LLM provider: %w", err)
			}

			runner := research.NewRunner(cfg, provider)
			output, err := runner.Run(context.Background(), research.Task{
				Type:    research.TypeEnrich,
				Topic:   docPath,
				Sources: []string{docPath},
			})
			if err != nil {
				return fmt.Errorf("enrich failed: %w", err)
			}

			fmt.Printf("Enriched document: %s\n", output)
			return nil
		},
	}

	cmd.Flags().StringVar(&backend, "backend", "", "LLM backend (claude, ollama)")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	return cmd
}
