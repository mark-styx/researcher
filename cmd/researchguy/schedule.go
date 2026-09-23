package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/scheduler"
	"github.com/robfig/cron/v3"
	"github.com/spf13/cobra"
)

func scheduleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Manage scheduled tasks",
		Long: `Manage recurring scheduled tasks. Scheduled tasks use cron expressions
and are executed by the daemon on their schedule.`,
		GroupID: "scheduling",
	}

	cmd.AddCommand(scheduleListCmd())
	cmd.AddCommand(scheduleAddCmd())
	cmd.AddCommand(scheduleRemoveCmd())
	return cmd
}

func scheduleListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List scheduled tasks",
		Example: `  researchguy schedule list`,
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
	var taskType, topic, cronExpr, backend, model string
	var priority int

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a scheduled task",
		Long: `Add a recurring scheduled task with a cron expression. Standard 5-field
cron format: minute hour day-of-month month day-of-week.
Task types: dive, watch, review, enrich, ask.`,
		Example: `  researchguy schedule add --type watch --topic "AI safety" --cron "0 9 * * 1"
  researchguy schedule add --type dive --topic "LLMs" --cron "0 0 1 * *"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if topic == "" {
				return fmt.Errorf("--topic is required")
			}
			if cronExpr == "" {
				return fmt.Errorf("--cron is required")
			}
			if err := validateTaskType(taskType); err != nil {
				return err
			}
			parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
			if _, err := parser.Parse(cronExpr); err != nil {
				return fmt.Errorf("invalid --cron expression %q: %w", cronExpr, err)
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
				Cron:      &cronExpr,
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

			fmt.Printf("Scheduled task %s: %s %q (cron: %s)\n", task.ID[:8], taskType, topic, cronExpr)
			return nil
		},
	}

	cmd.Flags().StringVar(&taskType, "type", "watch", "Task type (dive, watch, review, enrich, ask)")
	cmd.Flags().StringVar(&topic, "topic", "", "Research topic")
	cmd.Flags().StringVar(&cronExpr, "cron", "", "Cron expression")
	cmd.Flags().StringVar(&backend, "backend", "", "LLM backend override")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	cmd.Flags().IntVar(&priority, "priority", 0, "Task priority (higher = sooner)")
	return cmd
}

func scheduleRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <id>",
		Short:   "Remove a scheduled task",
		Example: `  researchguy schedule remove abc12345`,
		Args:    cobra.ExactArgs(1),
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
