package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestDaemonStatus_NotRunning(t *testing.T) {
	testSetup(t)

	out, err := runCmd(t, "daemon", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "not running") {
		t.Errorf("output missing 'not running': %s", out)
	}
}

func TestDaemonStatus_StalePID(t *testing.T) {
	configDir, _ := testSetup(t)

	// Write a PID file with a bogus PID
	pidPath := filepath.Join(configDir, "scheduler.pid")
	os.WriteFile(pidPath, []byte("999999999"), 0644)

	out, err := runCmd(t, "daemon", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "stale PID") || !strings.Contains(out, "not running") {
		t.Errorf("output missing stale PID indication: %s", out)
	}
}

func TestDaemonStop_NoPIDFile(t *testing.T) {
	testSetup(t)

	_, err := runCmd(t, "daemon", "stop")
	if err == nil {
		t.Fatal("expected error when no PID file exists")
	}
	if !strings.Contains(err.Error(), "daemon not running") && !strings.Contains(err.Error(), "PID file") {
		t.Errorf("error = %q, want to mention daemon/PID", err.Error())
	}
}

func TestDaemonStop_InvalidPID(t *testing.T) {
	configDir, _ := testSetup(t)

	pidPath := filepath.Join(configDir, "scheduler.pid")
	os.WriteFile(pidPath, []byte("not-a-number"), 0644)

	_, err := runCmd(t, "daemon", "stop")
	if err == nil {
		t.Fatal("expected error for invalid PID")
	}
	if !strings.Contains(err.Error(), "invalid PID") {
		t.Errorf("error = %q, want to contain 'invalid PID'", err.Error())
	}
}

func TestDaemonStop_StalePID(t *testing.T) {
	configDir, _ := testSetup(t)

	// Write a PID that definitely doesn't exist
	pidPath := filepath.Join(configDir, "scheduler.pid")
	os.WriteFile(pidPath, []byte("999999999"), 0644)

	_, err := runCmd(t, "daemon", "stop")
	if err == nil {
		t.Fatal("expected error for stale PID")
	}
	if !strings.Contains(err.Error(), "stale PID file") {
		t.Errorf("error = %q, want stale PID file message", err.Error())
	}
}

func TestDaemonStatus_InvalidPID(t *testing.T) {
	configDir, _ := testSetup(t)

	pidPath := filepath.Join(configDir, "scheduler.pid")
	os.WriteFile(pidPath, []byte("not-a-number"), 0644)

	out, err := runCmd(t, "daemon", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "invalid PID") {
		t.Errorf("output missing 'invalid PID': %s", out)
	}
}

func TestDaemonStatus_CurrentProcess(t *testing.T) {
	configDir, _ := testSetup(t)

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("getting executable: %v", err)
	}
	pidJSON, err := json.Marshal(map[string]any{
		"pid": os.Getpid(),
		"exe": exe,
	})
	if err != nil {
		t.Fatalf("marshaling pid json: %v", err)
	}

	// Write current process PID with executable metadata — it should be "running".
	pidPath := filepath.Join(configDir, "scheduler.pid")
	os.WriteFile(pidPath, pidJSON, 0644)

	out, err := runCmd(t, "daemon", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "running") {
		t.Errorf("output missing 'running': %s", out)
	}
}

func TestDaemonStatus_LegacyPIDOtherProcess(t *testing.T) {
	configDir, _ := testSetup(t)
	pidPath := filepath.Join(configDir, "scheduler.pid")
	pidJSON, err := json.Marshal(map[string]any{
		"pid": os.Getpid(),
		"exe": "/definitely/not/the/current/executable",
	})
	if err != nil {
		t.Fatalf("marshaling pid json: %v", err)
	}
	os.WriteFile(pidPath, pidJSON, 0644)

	out, err := runCmd(t, "daemon", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "belongs to another process") {
		t.Errorf("expected legacy PID mismatch output, got: %s", out)
	}
}

func TestDaemonSubcommands(t *testing.T) {
	root := buildRoot()

	var daemonC *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "daemon" {
			daemonC = c
			break
		}
	}
	if daemonC == nil {
		t.Fatal("daemon command not found")
	}

	expected := []string{"start", "stop", "status"}
	subs := daemonC.Commands()

	if len(subs) != len(expected) {
		t.Errorf("daemon has %d subcommands, want %d", len(subs), len(expected))
	}

	names := make(map[string]bool)
	for _, s := range subs {
		names[s.Name()] = true
	}

	for _, exp := range expected {
		if !names[exp] {
			t.Errorf("daemon missing subcommand %q", exp)
		}
	}
}
