package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchCmd(t *testing.T) {
	configDir, researchDir := testSetup(t)

	// Override the grepai binary to our fake script
	script := fakeGrepai(t)

	// Rewrite config with the fake grepai binary
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
  binary: %s
`, researchDir, configDir, configDir, script)

	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	out, err := runCmd(t, "search", "neural networks")
	if err != nil {
		t.Fatalf("search command failed: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "args:") {
		t.Errorf("expected output to contain grepai args, got: %s", out)
	}
	if !strings.Contains(out, "neural networks") {
		t.Errorf("expected output to contain query, got: %s", out)
	}
}

func TestSearchCmd_MissingQuery(t *testing.T) {
	testSetup(t)

	_, err := runCmd(t, "search")
	if err == nil {
		t.Fatal("expected error when no query argument provided")
	}
}
