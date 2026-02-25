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
		Long:  "A CLI that automates research workflows, schedules recurring tasks, indexes with grepai, and supports multiple LLM backends.",
	}

	root.AddCommand(versionCmd())
	root.AddCommand(initCmd())
	root.AddCommand(configCmd())
	root.AddCommand(askCmd())
	root.AddCommand(diveCmd())
	root.AddCommand(listCmd())
	root.AddCommand(showCmd())
	root.AddCommand(searchCmd())
	root.AddCommand(reviewCmd())
	root.AddCommand(enrichCmd())
	root.AddCommand(watchCmd())
	root.AddCommand(daemonCmd())
	root.AddCommand(scheduleCmd())
	root.AddCommand(queueCmd())
	root.AddCommand(linkCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("researcher", Version)
		},
	}
}
