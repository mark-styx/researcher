package research

import (
	"os"
	"path/filepath"
	"testing"
)

func setupResearchDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Create categories with files
	for _, cat := range []string{"llm", "security"} {
		os.MkdirAll(filepath.Join(dir, cat), 0755)
	}
	os.WriteFile(filepath.Join(dir, "llm", "agents.md"), []byte("# Agents"), 0644)
	os.WriteFile(filepath.Join(dir, "llm", "tools.md"), []byte("# Tools"), 0644)
	os.WriteFile(filepath.Join(dir, "security", "auth.md"), []byte("# Auth"), 0644)

	// Hidden dir should be ignored
	os.MkdirAll(filepath.Join(dir, ".grepai"), 0755)
	os.WriteFile(filepath.Join(dir, ".grepai", "index.md"), []byte("index"), 0644)

	// Non-md file should be ignored
	os.WriteFile(filepath.Join(dir, "llm", "notes.txt"), []byte("notes"), 0644)

	// Top-level file (not in a category dir) should be ignored
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("readme"), 0644)

	return dir
}

func TestListResearchFiles(t *testing.T) {
	dir := setupResearchDir(t)

	files, err := ListResearchFiles(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(files))
	}

	// Should be sorted by category then name
	expected := []struct{ cat, name string }{
		{"llm", "agents"},
		{"llm", "tools"},
		{"security", "auth"},
	}
	for i, exp := range expected {
		if files[i].Category != exp.cat || files[i].Name != exp.name {
			t.Errorf("files[%d] = {%q, %q}, want {%q, %q}",
				i, files[i].Category, files[i].Name, exp.cat, exp.name)
		}
		if files[i].Path == "" {
			t.Errorf("files[%d].Path is empty", i)
		}
		if files[i].Size == 0 {
			t.Errorf("files[%d].Size is 0", i)
		}
	}
}

func TestListResearchFiles_Empty(t *testing.T) {
	dir := t.TempDir()

	files, err := ListResearchFiles(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

func TestListCategories(t *testing.T) {
	dir := setupResearchDir(t)

	cats, err := ListCategories(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cats) != 2 {
		t.Fatalf("expected 2 categories, got %d: %v", len(cats), cats)
	}
	if cats[0] != "llm" || cats[1] != "security" {
		t.Errorf("categories = %v, want [llm, security]", cats)
	}
}

func TestListCategories_Empty(t *testing.T) {
	dir := t.TempDir()

	cats, err := ListCategories(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cats) != 0 {
		t.Errorf("expected 0 categories, got %d", len(cats))
	}
}

func TestListCategoryFiles(t *testing.T) {
	dir := setupResearchDir(t)

	files, err := ListCategoryFiles(dir, "llm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].Name != "agents" || files[1].Name != "tools" {
		t.Errorf("files = [%q, %q], want [agents, tools]", files[0].Name, files[1].Name)
	}
	for _, f := range files {
		if f.Category != "llm" {
			t.Errorf("file %q has category %q, want llm", f.Name, f.Category)
		}
	}
}

func TestListCategoryFiles_NonExistent(t *testing.T) {
	dir := t.TempDir()

	_, err := ListCategoryFiles(dir, "nonexistent")
	if err == nil {
		t.Error("expected error for non-existent category")
	}
}
