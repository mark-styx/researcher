package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- list ---

func TestListCmd(t *testing.T) {
	_, researchDir := testSetup(t)

	// Create two category dirs with .md files
	for _, cat := range []string{"alpha", "beta"} {
		dir := filepath.Join(researchDir, cat)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			f := filepath.Join(dir, fmt.Sprintf("doc%d.md", i))
			os.WriteFile(f, []byte("# test"), 0644)
		}
	}

	out, err := runCmd(t, "list")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "alpha") {
		t.Errorf("output missing 'alpha': %s", out)
	}
	if !strings.Contains(out, "beta") {
		t.Errorf("output missing 'beta': %s", out)
	}
	if !strings.Contains(out, "doc0") {
		t.Errorf("output missing file name 'doc0': %s", out)
	}
}

func TestListCmd_EmptyDir(t *testing.T) {
	testSetup(t)

	out, err := runCmd(t, "list")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No research files found") {
		t.Errorf("output missing empty message: %s", out)
	}
}

// --- show ---

func TestShowCmd(t *testing.T) {
	_, researchDir := testSetup(t)

	projDir := filepath.Join(researchDir, "myproject")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "README.md"), []byte("hello"), 0644)

	out, err := runCmd(t, "show", "myproject")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "myproject") {
		t.Errorf("output missing project name: %s", out)
	}
	if !strings.Contains(out, "README.md") {
		t.Errorf("output missing filename: %s", out)
	}
}

func TestShowCmd_NotFound(t *testing.T) {
	testSetup(t)

	_, err := runCmd(t, "show", "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent project")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want to contain 'not found'", err.Error())
	}
}

// --- link ---

func TestLinkCmd(t *testing.T) {
	_, researchDir := testSetup(t)

	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "mylink")

	out, err := runCmd(t, "link", target)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "Linked") {
		t.Errorf("output missing 'Linked': %s", out)
	}

	// Verify symlink exists and points to research dir
	dest, err := os.Readlink(target)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if dest != researchDir {
		t.Errorf("symlink target = %q, want %q", dest, researchDir)
	}
}

func TestLinkCmd_RelativePath(t *testing.T) {
	_, researchDir := testSetup(t)

	// Use a relative path — cmd needs to resolve it to absolute
	tmpDir := t.TempDir()
	// Change to tmpDir so relative path resolves there
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	out, err := runCmd(t, "link", "relative-link")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Linked") {
		t.Errorf("output missing 'Linked': %s", out)
	}

	// Verify the symlink was created at the absolute path
	target := filepath.Join(tmpDir, "relative-link")
	dest, err := os.Readlink(target)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if dest != researchDir {
		t.Errorf("symlink target = %q, want %q", dest, researchDir)
	}
}

func TestLinkCmd_MissingArg(t *testing.T) {
	testSetup(t)

	_, err := runCmd(t, "link")
	if err == nil {
		t.Fatal("expected error for missing argument")
	}
}

func TestLinkCmd_AlreadyExists(t *testing.T) {
	testSetup(t)

	target := t.TempDir() // this already exists

	_, err := runCmd(t, "link", target)
	if err == nil {
		t.Fatal("expected error for existing target")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q, want to contain 'already exists'", err.Error())
	}
}

// --- init ---

func TestInitCmd(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("RESEARCHER_CONFIG_DIR", configDir)

	// Let init create the default config. It may try grepai but handles failure gracefully.
	out, err := runCmd(t, "init")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should contain "Created config" since there's no existing config.yaml
	if !strings.Contains(out, "Created config") {
		t.Errorf("output missing 'Created config': %s", out)
	}
	if !strings.Contains(out, "initialized") {
		t.Errorf("output missing 'initialized': %s", out)
	}

	// Verify config.yaml was created
	if _, err := os.Stat(filepath.Join(configDir, "config.yaml")); err != nil {
		t.Errorf("config.yaml not created: %v", err)
	}
}

func TestInitCmd_AlreadyExists(t *testing.T) {
	testSetup(t)

	out, err := runCmd(t, "init")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "already exists") {
		t.Errorf("output missing 'already exists': %s", out)
	}
}

func TestInitCmd_GrepaiEnabled(t *testing.T) {
	configDir := t.TempDir()
	researchDir := t.TempDir()
	t.Setenv("RESEARCHER_CONFIG_DIR", configDir)

	// Write config with auto_index: true and binary: echo (which will always succeed)
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
  auto_index: true
  binary: echo
`, researchDir, configDir, configDir)

	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	out, err := runCmd(t, "init")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should mention grepai since auto_index is true
	if !strings.Contains(out, "grepai") {
		t.Errorf("output should mention grepai with auto_index=true: %s", out)
	}
	if !strings.Contains(out, "initialized") {
		t.Errorf("output missing 'initialized': %s", out)
	}
}

func TestInitCmd_GrepaiDisabled(t *testing.T) {
	testSetup(t) // config has auto_index: false

	out, err := runCmd(t, "init")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should not contain grepai-related output since auto_index=false
	if strings.Contains(out, "grepai") {
		t.Errorf("grepai should not appear in output when auto_index=false: %s", out)
	}
}
