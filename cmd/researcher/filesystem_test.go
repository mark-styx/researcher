package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- list ---

func TestListCmd(t *testing.T) {
	_, researchDir := testSetup(t)

	// Create two project dirs with .md files
	for _, proj := range []string{"alpha", "beta"} {
		dir := filepath.Join(researchDir, proj)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			f := filepath.Join(dir, "doc"+string(rune('0'+i))+".md")
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
	if !strings.Contains(out, "2") {
		t.Errorf("output missing file count '2': %s", out)
	}
}

func TestListCmd_EmptyDir(t *testing.T) {
	testSetup(t)

	out, err := runCmd(t, "list")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have header but no project rows
	if !strings.Contains(out, "PROJECT") {
		t.Errorf("output missing header: %s", out)
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
