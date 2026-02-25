package main

import (
	"context"
	"fmt"

	"github.com/marklubin/researcher/internal/config"
	"github.com/marklubin/researcher/internal/llm"
	"github.com/marklubin/researcher/internal/research"
	"github.com/marklubin/researcher/internal/tools"
	"github.com/spf13/cobra"
)

func askCmd() *cobra.Command {
	var backend, model string

	cmd := &cobra.Command{
		Use:   "ask <question>",
		Short: "Quick one-shot question, prints answer to stdout",
		Long: `Ask a one-shot question and print the answer to stdout. Uses the configured
LLM backend with optional web search and tool use. The answer is not saved
to the research directory.`,
		Example: `  researcher ask "What is quantum computing?"
  researcher ask "Compare TCP vs UDP" --backend ollama --model llama3`,
		GroupID: "research",
		Args:    cobra.ExactArgs(1),
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

			var tt []tools.Tool
			if cfg.Tools.Enabled {
				tt = tools.DefaultTools()
			}

			resp, err := provider.Complete(context.Background(), llm.Request{
				SystemPrompt: research.SystemPrompt(research.TypeAsk),
				UserPrompt:   question,
				MaxTokens:    cfg.Claude.MaxTokens,
				Tools:        tt,
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
