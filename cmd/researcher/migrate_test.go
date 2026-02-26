package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateCmd_NoCandidates(t *testing.T) {
	testSetup(t)

	out, err := runCmd(t, "migrate", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No directories to migrate") {
		t.Errorf("expected 'No directories to migrate', got: %s", out)
	}
}

func TestMigrateCmd_DryRun(t *testing.T) {
	_, researchDir := testSetupWithOllama(t, `{"category": "llm", "filename": "test-topic"}`)

	// Create a flat directory with README.md
	flatDir := filepath.Join(researchDir, "some-long-slug-topic-name")
	os.MkdirAll(flatDir, 0755)
	os.WriteFile(filepath.Join(flatDir, "README.md"), []byte("# Some Long Topic Name\n\nContent here."), 0644)

	out, err := runCmd(t, "migrate", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "[dry-run]") {
		t.Errorf("expected dry-run output, got: %s", out)
	}
	if !strings.Contains(out, "llm/test-topic.md") {
		t.Errorf("expected target path in output, got: %s", out)
	}

	// Verify file was NOT moved
	if _, err := os.Stat(filepath.Join(flatDir, "README.md")); err != nil {
		t.Error("file should not have been moved in dry-run mode")
	}
}

func TestMigrateCmd(t *testing.T) {
	_, researchDir := testSetupWithOllama(t, `{"category": "llm", "filename": "test-topic"}`)

	// Create a flat directory with README.md
	flatDir := filepath.Join(researchDir, "some-long-slug-topic-name")
	os.MkdirAll(flatDir, 0755)
	os.WriteFile(filepath.Join(flatDir, "README.md"), []byte("# Some Long Topic Name\n\nContent here."), 0644)

	out, err := runCmd(t, "migrate")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Migrated") {
		t.Errorf("expected 'Migrated' in output, got: %s", out)
	}

	// Verify file was moved
	targetPath := filepath.Join(researchDir, "llm", "test-topic.md")
	if _, err := os.Stat(targetPath); err != nil {
		t.Errorf("expected migrated file at %s: %v", targetPath, err)
	}

	// Verify old directory was removed
	if _, err := os.Stat(flatDir); !os.IsNotExist(err) {
		t.Error("old directory should have been removed")
	}
}

func TestMigrateCmd_SkipsOrganizedDirs(t *testing.T) {
	_, researchDir := testSetupWithOllama(t, `{"category": "llm", "filename": "test"}`)

	// Create an already-organized category dir with multiple files
	catDir := filepath.Join(researchDir, "llm")
	os.MkdirAll(catDir, 0755)
	os.WriteFile(filepath.Join(catDir, "agentic-code.md"), []byte("content"), 0644)
	os.WriteFile(filepath.Join(catDir, "models.md"), []byte("content"), 0644)

	out, err := runCmd(t, "migrate", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should not try to migrate already-organized directories
	if !strings.Contains(out, "No directories to migrate") {
		t.Errorf("expected no candidates, got: %s", out)
	}
}

func TestExtractTopic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "README.md")

	os.WriteFile(path, []byte("# My Research Topic\n\nContent here."), 0644)
	got := extractTopic(path)
	if got != "My Research Topic" {
		t.Errorf("got %q, want %q", got, "My Research Topic")
	}
}

func TestExtractTopic_NoHeading(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "README.md")

	os.WriteFile(path, []byte("Just some content without a heading."), 0644)
	got := extractTopic(path)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestFindMigrationCandidates(t *testing.T) {
	dir := t.TempDir()

	// Candidate: dir with exactly README.md
	cand := filepath.Join(dir, "long-slug-name")
	os.MkdirAll(cand, 0755)
	os.WriteFile(filepath.Join(cand, "README.md"), []byte("# Topic\nContent"), 0644)

	// Not a candidate: dir with multiple files
	multi := filepath.Join(dir, "llm")
	os.MkdirAll(multi, 0755)
	os.WriteFile(filepath.Join(multi, "file1.md"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(multi, "file2.md"), []byte("y"), 0644)

	// Not a candidate: dir with non-README single file
	other := filepath.Join(dir, "other")
	os.MkdirAll(other, 0755)
	os.WriteFile(filepath.Join(other, "notes.md"), []byte("x"), 0644)

	candidates, err := findMigrationCandidates(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].dirName != "long-slug-name" {
		t.Errorf("dirName = %q, want %q", candidates[0].dirName, "long-slug-name")
	}
	if candidates[0].topic != "Topic" {
		t.Errorf("topic = %q, want %q", candidates[0].topic, "Topic")
	}
}
