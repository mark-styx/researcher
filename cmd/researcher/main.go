package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var Version = "dev"

func main() {
	root := &cobra.Command{
		Use:   "researcher",
		Short: "Automated research workflows with LLM backends",
		Long: `Researcher is a CLI that automates research workflows using LLM backends.

It can answer one-shot questions, generate comprehensive research reports,
synthesize literature reviews, and enrich existing documents. Research output
is saved as structured markdown in a configurable research directory.

Supports Claude and Ollama backends, recurring task scheduling via cron,
semantic search via grepai, and web-augmented tool use.

Get started:
  researcher init          Set up config and directories
  researcher ask "..."     Quick question
  researcher dive "..."    Full research report`,
	}

	root.AddGroup(
		&cobra.Group{ID: "research", Title: "Research Commands:"},
		&cobra.Group{ID: "project", Title: "Project Management:"},
		&cobra.Group{ID: "scheduling", Title: "Task Scheduling:"},
		&cobra.Group{ID: "setup", Title: "Setup:"},
	)

	root.AddCommand(askCmd())
	root.AddCommand(diveCmd())
	root.AddCommand(reviewCmd())
	root.AddCommand(enrichCmd())
	root.AddCommand(searchCmd())
	root.AddCommand(listCmd())
	root.AddCommand(showCmd())
	root.AddCommand(linkCmd())
	root.AddCommand(watchCmd())
	root.AddCommand(queueCmd())
	root.AddCommand(scheduleCmd())
	root.AddCommand(daemonCmd())
	root.AddCommand(migrateCmd())
	root.AddCommand(initCmd())
	root.AddCommand(configCmd())
	root.AddCommand(versionCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Short:   "Print version",
		GroupID: "setup",
		Example: `  researcher version`,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("researcher", Version)
		},
	}
}
