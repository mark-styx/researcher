package main

import (
	"context"
	"fmt"

	"github.com/marklubin/researcher/internal/config"
	"github.com/marklubin/researcher/internal/llm"
	"github.com/spf13/cobra"
)

func askCmd() *cobra.Command {
	var backend, model string

	cmd := &cobra.Command{
		Use:   "ask <question>",
		Short: "Quick one-shot question, prints answer to stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			question := args[0]

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			provider, err := llm.NewProvider(cfg, backend, model)
			if err != nil {
				return fmt.Errorf("creating LLM provider: %w", err)
			}

			resp, err := provider.Complete(context.Background(), llm.Request{
				SystemPrompt: "You are a knowledgeable research assistant. Provide clear, accurate, and well-structured answers.",
				UserPrompt:   question,
				MaxTokens:    cfg.Claude.MaxTokens,
			})
			if err != nil {
				return fmt.Errorf("LLM call failed: %w", err)
			}

			fmt.Println(resp)
			return nil
		},
	}

	cmd.Flags().StringVar(&backend, "backend", "", "LLM backend (claude, ollama)")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	return cmd
}
