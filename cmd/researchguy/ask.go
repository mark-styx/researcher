package main

import (
	"context"
	"fmt"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/research"
	"github.com/spf13/cobra"
)

func askCmd() *cobra.Command {
	var backend, model, maxAge string
	var noSave, noResearch, asJSON bool
	var projects []string

	cmd := &cobra.Command{
		Use:   "ask <question>",
		Short: "Quick one-shot question with research context, saves answer",
		Long: `Ask a question and get an answer informed by your existing research.

By default, the command:
1. Searches existing research via grepai for relevant context
2. Filters out stale results (configurable, default 90 days)
3. Sends the question + research context to the LLM
4. Saves the answer to the research directory

Use --no-save to skip saving, --no-research to skip the research lookup,
or --max-age to control the freshness filter.`,
		Example: `  researchguy ask "What is quantum computing?"
  researchguy ask "Compare TCP vs UDP" --backend ollama --model llama3
  researchguy ask "What are the best open source LLMs?" --max-age 30d
  researchguy ask "Quick question" --no-save --no-research`,
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

			runner := research.NewRunner(cfg, provider)
			task := research.Task{
				Type:       research.TypeAsk,
				Topic:      question,
				NoSave:     noSave,
				NoResearch: noResearch,
				MaxAge:     maxAge,
				Projects:   projects,
				Quiet:      asJSON,
			}

			result, err := runner.Run(context.Background(), task)
			if err != nil {
				return fmt.Errorf("ask failed: %w", err)
			}

			if asJSON {
				return printJSON(result.Fields("answer", provider.Name()))
			}
			if result.FilePath != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Answer saved to: %s\n", result.FilePath)
			}
			// The answer itself is on stdout, so status lines go to stderr.
			printRunRecord(cmd.ErrOrStderr(), result.RunDir)

			return nil
		},
	}

	cmd.Flags().StringVar(&backend, "backend", "", "LLM backend (claude, ollama, codex, goose, hybrid)")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	cmd.Flags().BoolVar(&noSave, "no-save", false, "Don't save the answer to the research directory")
	cmd.Flags().BoolVar(&noResearch, "no-research", false, "Skip searching existing research for context")
	cmd.Flags().StringVar(&maxAge, "max-age", "", "Max age for research freshness filter (e.g. 90d, 2w, 24h, none)")
	cmd.Flags().StringSliceVar(&projects, "project", nil, "grepai workspace project to search for context (repeatable; default: grepai.projects)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print JSON {answer, saved_to, backend, metadata, run_id, run_dir} instead of the answer text")
	return cmd
}
