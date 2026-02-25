package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/marklubin/researcher/internal/config"
	"github.com/marklubin/researcher/internal/llm"
	"github.com/marklubin/researcher/internal/research"
	"github.com/spf13/cobra"
)

func reviewCmd() *cobra.Command {
	var backend, model, sources string

	cmd := &cobra.Command{
		Use:   "review <topic>",
		Short: "Literature review / synthesis on a topic",
		Args:  cobra.ExactArgs(1),
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
			output, err := runner.Run(context.Background(), research.Task{
				Type:    research.TypeReview,
				Topic:   topic,
				Sources: sourceFiles,
			})
			if err != nil {
				return fmt.Errorf("review failed: %w", err)
			}

			fmt.Printf("Review saved to: %s\n", output)
			return nil
		},
	}

	cmd.Flags().StringVar(&backend, "backend", "", "LLM backend (claude, ollama)")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	cmd.Flags().StringVar(&sources, "sources", "", "Comma-separated source files")
	return cmd
}
