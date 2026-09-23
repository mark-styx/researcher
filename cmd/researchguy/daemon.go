package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/scheduler"
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
		Example: `  researchguy daemon start`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			pidPath := config.ExpandPath(cfg.Scheduler.PIDFile)
			if err := os.MkdirAll(filepath.Dir(pidPath), 0755); err != nil {
				return fmt.Errorf("creating PID directory: %w", err)
			}
			if running, pid := daemonRunning(pidPath); running {
				return fmt.Errorf("daemon already running (PID: %d)", pid)
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
			exe, _ := os.Executable()
			info := pidInfo{
				PID:       os.Getpid(),
				Exe:       exe,
				StartedAt: time.Now().Unix(),
			}
			pidData, err := json.Marshal(info)
			if err != nil {
				return fmt.Errorf("encoding PID file: %w", err)
			}
			if err := os.WriteFile(pidPath, pidData, 0644); err != nil {
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
		Example: `  researchguy daemon stop`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			pidPath := config.ExpandPath(cfg.Scheduler.PIDFile)
			info, err := readPIDFile(pidPath)
			if err != nil {
				return fmt.Errorf("reading PID file (daemon not running?): %w", err)
			}

			proc, alive := processAlive(info.PID)
			if !alive {
				return fmt.Errorf("daemon not running (stale PID file)")
			}
			if !pidMatchesProcess(info) {
				return fmt.Errorf("PID %d belongs to a different process", info.PID)
			}

			if err := proc.Signal(syscall.SIGTERM); err != nil {
				return fmt.Errorf("sending signal: %w", err)
			}

			fmt.Printf("Sent SIGTERM to daemon (PID: %d)\n", info.PID)
			return nil
		},
	}
}

func daemonStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Short:   "Show daemon status",
		Long:    `Check whether the scheduler daemon is currently running.`,
		Example: `  researchguy daemon status`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			pidPath := config.ExpandPath(cfg.Scheduler.PIDFile)
			info, err := readPIDFile(pidPath)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Println("Daemon: not running")
					return nil
				}
				if strings.Contains(err.Error(), "invalid PID") || strings.Contains(err.Error(), "invalid PID file") {
					fmt.Println("Daemon: unknown (invalid PID file)")
					return nil
				}
				fmt.Println("Daemon: not running")
				return nil
			}

			_, alive := processAlive(info.PID)
			if !alive {
				fmt.Println("Daemon: not running (stale PID file)")
				return nil
			}

			if !pidMatchesProcess(info) {
				fmt.Printf("Daemon: not running (PID %d belongs to another process)\n", info.PID)
				return nil
			}

			fmt.Printf("Daemon: running (PID: %d)\n", info.PID)
			return nil
		},
	}
}

type pidInfo struct {
	PID       int    `json:"pid"`
	Exe       string `json:"exe,omitempty"`
	StartedAt int64  `json:"started_at,omitempty"`
}

func readPIDFile(path string) (pidInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return pidInfo{}, err
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return pidInfo{}, fmt.Errorf("empty PID file")
	}
	// Backward compatibility with legacy integer-only PID files.
	if raw[0] != '{' {
		pid, err := strconv.Atoi(raw)
		if err != nil {
			return pidInfo{}, fmt.Errorf("invalid PID: %w", err)
		}
		return pidInfo{PID: pid}, nil
	}

	var info pidInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return pidInfo{}, fmt.Errorf("invalid PID file JSON: %w", err)
	}
	if info.PID <= 0 {
		return pidInfo{}, fmt.Errorf("invalid PID: %d", info.PID)
	}
	return info, nil
}

func processAlive(pid int) (*os.Process, bool) {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil, false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return nil, false
	}
	return proc, true
}

func processCommandLine(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func pidMatchesProcess(info pidInfo) bool {
	cmdline := processCommandLine(info.PID)
	if cmdline == "" {
		return false
	}
	if info.Exe != "" {
		return strings.Contains(cmdline, filepath.Base(info.Exe))
	}
	// Legacy PID file fallback.
	return strings.Contains(cmdline, "researchguy")
}

func daemonRunning(pidPath string) (bool, int) {
	info, err := readPIDFile(pidPath)
	if err != nil {
		return false, 0
	}
	if _, alive := processAlive(info.PID); !alive {
		return false, 0
	}
	if !pidMatchesProcess(info) {
		return false, 0
	}
	return true, info.PID
}
