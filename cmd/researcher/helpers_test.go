package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// testSetup creates temp config + research dirs, writes a minimal config.yaml,
// and sets RESEARCHER_CONFIG_DIR so that config.Dir()/Load() use the temp dirs.
// Returns the config dir path, research dir path, and a cleanup function.
func testSetup(t *testing.T) (configDir, researchDir string) {
	t.Helper()

	configDir = t.TempDir()
	researchDir = t.TempDir()

	yaml := fmt.Sprintf(`research_dir: %s
default_backend: ollama
claude:
  binary: echo
  model: test
  max_tokens: 100
ollama:
  host: http://127.0.0.1:0
  model: test
  fallback_model: test
tools:
  enabled: false
  max_iterations: 1
  max_results: 1
scheduler:
  poll_interval: 60s
  max_concurrent: 1
  log_file: %s/scheduler.log
  pid_file: %s/scheduler.pid
grepai:
  auto_index: false
  binary: echo
`, researchDir, configDir, configDir)

	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	t.Setenv("RESEARCHER_CONFIG_DIR", configDir)

	return configDir, researchDir
}

// ollamaResponse is the JSON structure returned by the mock Ollama server.
type ollamaResponse struct {
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Done bool `json:"done"`
}

// ollamaServer starts an httptest server that returns the given content
// as an Ollama /api/chat response. Returns the server URL.
func ollamaServer(t *testing.T, content string) string {
	t.Helper()

	resp := ollamaResponse{Done: true}
	resp.Message.Role = "assistant"
	resp.Message.Content = content

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshaling mock response: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
	t.Cleanup(srv.Close)

	return srv.URL
}

// buildRoot constructs the full cobra root command with all subcommands,
// mirroring main().
func buildRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "researcher",
		Short: "Automated research workflows with LLM backends",
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
	root.AddCommand(mcpCmd())
	root.AddCommand(versionCmd())

	return root
}

// runCmd builds the root command, sets args, captures real os.Stdout
// (since commands use fmt.Println, not cmd.OutOrStdout), executes,
// and returns the combined output string and any error.
func runCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()

	// Capture real os.Stdout since commands use fmt.Println/Printf
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	os.Stdout = w
	os.Stderr = w

	root := buildRoot()
	root.SetOut(w)
	root.SetErr(w)
	root.SetArgs(args)

	execErr := root.Execute()

	w.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	var buf bytes.Buffer
	buf.ReadFrom(r)
	r.Close()

	return buf.String(), execErr
}

// fakeGrepai creates a shell script in a temp dir that echoes its arguments.
// Returns the path to the script.
func fakeGrepai(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-grepai")
	err := os.WriteFile(script, []byte("#!/bin/sh\necho \"args: $*\"\n"), 0755)
	if err != nil {
		t.Fatalf("writing fake grepai: %v", err)
	}
	return script
}
