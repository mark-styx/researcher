package main

import (
	"fmt"
	"time"

	"github.com/marklubin/researcher/internal/config"
	"github.com/marklubin/researcher/internal/scheduler"
	"github.com/spf13/cobra"
)

func watchCmd() *cobra.Command {
	var cron, backend, model string

	cmd := &cobra.Command{
		Use:   "watch <topic>",
		Short: "Schedule recurring monitoring of a topic",
		Long: `Schedule recurring monitoring of a research topic. Creates a scheduled task
that runs on the given cron schedule. The daemon must be running to execute
scheduled tasks (see 'researcher daemon start').`,
		Example: `  researcher watch "AI safety developments"
  researcher watch "quantum computing" --cron "0 9 * * 1"
  researcher watch "LLM benchmarks" --cron "0 0 1 * *" --backend ollama`,
		GroupID: "scheduling",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			topic := args[0]

			if cron == "" {
				cron = "0 9 * * 1" // default: Monday 9am
			}

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			store, err := scheduler.NewStore(cfg)
			if err != nil {
				return fmt.Errorf("opening task store: %w", err)
			}
			defer store.Close()

			task := &scheduler.Task{
				Type:      "watch",
				Topic:     topic,
				Status:    scheduler.StatusScheduled,
				Cron:      &cron,
				CreatedAt: time.Now(),
			}
			if backend != "" {
				task.Backend = &backend
			}
			if model != "" {
				task.Model = &model
			}

			if err := store.Create(task); err != nil {
				return fmt.Errorf("creating watch task: %w", err)
			}

			fmt.Printf("Scheduled watch for %q with cron %q (id: %s)\n", topic, cron, task.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&cron, "cron", "0 9 * * 1", "Cron expression for schedule")
	cmd.Flags().StringVar(&backend, "backend", "", "LLM backend override")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	return cmd
}
