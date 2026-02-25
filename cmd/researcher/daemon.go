package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/marklubin/researcher/internal/config"
	"github.com/marklubin/researcher/internal/llm"
	"github.com/marklubin/researcher/internal/scheduler"
	"github.com/spf13/cobra"
)

func daemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the scheduler daemon",
		Long: `Manage the background scheduler daemon. The daemon processes queued tasks
and executes scheduled cron tasks. It must be running for any scheduled
or queued research to execute.`,
		GroupID: "scheduling",
	}

	cmd.AddCommand(daemonStartCmd())
	cmd.AddCommand(daemonStopCmd())
	cmd.AddCommand(daemonStatusCmd())
	return cmd
}

func daemonStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the scheduler daemon (foreground)",
		Long: `Start the scheduler daemon in the foreground. It polls the task store
for queued and scheduled tasks and executes them using the configured
LLM backend. Send SIGINT or SIGTERM to stop.`,
		Example: `  researcher daemon start`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			provider, err := llm.NewProvider(cfg, "", "")
			if err != nil {
				return fmt.Errorf("creating LLM provider: %w", err)
			}

			sched, err := scheduler.New(cfg, provider)
			if err != nil {
				return fmt.Errorf("creating scheduler: %w", err)
			}

			// Write PID file
			pidPath := config.ExpandPath(cfg.Scheduler.PIDFile)
			if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
				return fmt.Errorf("writing PID file: %w", err)
			}
			defer os.Remove(pidPath)

			fmt.Println("Scheduler daemon started (PID:", os.Getpid(), ")")

			// Handle shutdown signals
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			go func() {
				<-sigCh
				fmt.Println("\nShutting down...")
				sched.Stop()
			}()

			sched.Run()
			return nil
		},
	}
}

func daemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "stop",
		Short:   "Stop the scheduler daemon",
		Long:    `Send SIGTERM to the running scheduler daemon using the PID file.`,
		Example: `  researcher daemon stop`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			pidPath := config.ExpandPath(cfg.Scheduler.PIDFile)
			data, err := os.ReadFile(pidPath)
			if err != nil {
				return fmt.Errorf("reading PID file (daemon not running?): %w", err)
			}

			pid, err := strconv.Atoi(string(data))
			if err != nil {
				return fmt.Errorf("invalid PID: %w", err)
			}

			proc, err := os.FindProcess(pid)
			if err != nil {
				return fmt.Errorf("finding process: %w", err)
			}

			if err := proc.Signal(syscall.SIGTERM); err != nil {
				return fmt.Errorf("sending signal: %w", err)
			}

			fmt.Printf("Sent SIGTERM to daemon (PID: %d)\n", pid)
			return nil
		},
	}
}

func daemonStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Short:   "Show daemon status",
		Long:    `Check whether the scheduler daemon is currently running.`,
		Example: `  researcher daemon status`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			pidPath := config.ExpandPath(cfg.Scheduler.PIDFile)
			data, err := os.ReadFile(pidPath)
			if err != nil {
				fmt.Println("Daemon: not running")
				return nil
			}

			pid, err := strconv.Atoi(string(data))
			if err != nil {
				fmt.Println("Daemon: unknown (invalid PID file)")
				return nil
			}

			proc, err := os.FindProcess(pid)
			if err != nil {
				fmt.Println("Daemon: not running (stale PID file)")
				return nil
			}

			// On Unix, signal 0 checks if process exists
			if err := proc.Signal(syscall.Signal(0)); err != nil {
				fmt.Println("Daemon: not running (stale PID file)")
				return nil
			}

			fmt.Printf("Daemon: running (PID: %d)\n", pid)
			return nil
		},
	}
}
