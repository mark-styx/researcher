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

func queueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "Manage one-shot task queue",
		Long: `Manage the one-shot task queue. Queued tasks run once and are picked up
by the daemon in priority order. Use this to batch research tasks for
background processing.`,
		GroupID: "scheduling",
	}

	cmd.AddCommand(queueListCmd())
	cmd.AddCommand(queueAddCmd())
	return cmd
}

func queueListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "Show pending queued tasks",
		Example: `  researcher queue list`,
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

			tasks, err := store.ListQueued()
			if err != nil {
				return fmt.Errorf("listing tasks: %w", err)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tTYPE\tTOPIC\tPRIORITY\tSTATUS")
			for _, t := range tasks {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n",
					t.ID[:8], t.Type, t.Topic, t.Priority, t.Status)
			}
			w.Flush()
			return nil
		},
	}
}

func queueAddCmd() *cobra.Command {
	var taskType, topic, backend, model string
	var priority int

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a one-shot task to the queue",
		Long: `Add a one-shot research task to the queue. Task types: dive, watch,
review, enrich, ask. The daemon processes queued tasks in priority order.`,
		Example: `  researcher queue add --type dive --topic "quantum computing"
  researcher queue add --type review --topic "AI safety" --priority 10`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if topic == "" {
				return fmt.Errorf("--topic is required")
			}
			if err := validateTaskType(taskType); err != nil {
				return err
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
				Status:    scheduler.StatusQueued,
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

			fmt.Printf("Queued task %s: %s %q\n", task.ID[:8], taskType, topic)
			return nil
		},
	}

	cmd.Flags().StringVar(&taskType, "type", "dive", "Task type (dive, watch, review, enrich, ask)")
	cmd.Flags().StringVar(&topic, "topic", "", "Research topic")
	cmd.Flags().StringVar(&backend, "backend", "", "LLM backend override")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	cmd.Flags().IntVar(&priority, "priority", 0, "Task priority (higher = sooner)")
	return cmd
}
