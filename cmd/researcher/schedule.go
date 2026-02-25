package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/marklubin/researcher/internal/config"
	"github.com/marklubin/researcher/internal/scheduler"
	"github.com/spf13/cobra"
)

func scheduleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Manage scheduled tasks",
	}

	cmd.AddCommand(scheduleListCmd())
	cmd.AddCommand(scheduleAddCmd())
	cmd.AddCommand(scheduleRemoveCmd())
	return cmd
}

func scheduleListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List scheduled tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			store, err := scheduler.NewStore(cfg)
			if err != nil {
				return fmt.Errorf("opening store: %w", err)
			}
			defer store.Close()

			tasks, err := store.ListScheduled()
			if err != nil {
				return fmt.Errorf("listing tasks: %w", err)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tTYPE\tTOPIC\tCRON\tLAST RUN\tSTATUS")
			for _, t := range tasks {
				cron := ""
				if t.Cron != nil {
					cron = *t.Cron
				}
				lastRun := "never"
				if t.LastRunAt != nil {
					lastRun = t.LastRunAt.Format("2006-01-02 15:04")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					t.ID[:8], t.Type, t.Topic, cron, lastRun, t.Status)
			}
			w.Flush()
			return nil
		},
	}
}

func scheduleAddCmd() *cobra.Command {
	var taskType, topic, cron, backend, model string
	var priority int

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a scheduled task",
		RunE: func(cmd *cobra.Command, args []string) error {
			if topic == "" {
				return fmt.Errorf("--topic is required")
			}
			if cron == "" {
				return fmt.Errorf("--cron is required")
			}

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			store, err := scheduler.NewStore(cfg)
			if err != nil {
				return fmt.Errorf("opening store: %w", err)
			}
			defer store.Close()

			task := &scheduler.Task{
				Type:      taskType,
				Topic:     topic,
				Status:    scheduler.StatusScheduled,
				Cron:      &cron,
				Priority:  priority,
				CreatedAt: time.Now(),
			}
			if backend != "" {
				task.Backend = &backend
			}
			if model != "" {
				task.Model = &model
			}

			if err := store.Create(task); err != nil {
				return fmt.Errorf("creating task: %w", err)
			}

			fmt.Printf("Scheduled task %s: %s %q (cron: %s)\n", task.ID[:8], taskType, topic, cron)
			return nil
		},
	}

	cmd.Flags().StringVar(&taskType, "type", "watch", "Task type (dive, watch, review, enrich, ask)")
	cmd.Flags().StringVar(&topic, "topic", "", "Research topic")
	cmd.Flags().StringVar(&cron, "cron", "", "Cron expression")
	cmd.Flags().StringVar(&backend, "backend", "", "LLM backend override")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	cmd.Flags().IntVar(&priority, "priority", 0, "Task priority (higher = sooner)")
	return cmd
}

func scheduleRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "Remove a scheduled task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			store, err := scheduler.NewStore(cfg)
			if err != nil {
				return fmt.Errorf("opening store: %w", err)
			}
			defer store.Close()

			if err := store.Delete(id); err != nil {
				return fmt.Errorf("deleting task: %w", err)
			}

			fmt.Printf("Removed task %s\n", id)
			return nil
		},
	}
}
