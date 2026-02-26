package research

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

var errMock = fmt.Errorf("mock error")

func TestCategorize_Success(t *testing.T) {
	mock := &mockProvider{response: `{"category": "llm", "filename": "open-source-models"}`}
	loc, err := Categorize(context.Background(), mock, "best open source LLM models", []string{"llm", "adblock"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loc.Category != "llm" {
		t.Errorf("category = %q, want %q", loc.Category, "llm")
	}
	if loc.Filename != "open-source-models" {
		t.Errorf("filename = %q, want %q", loc.Filename, "open-source-models")
	}
}

func TestCategorize_MarkdownFencing(t *testing.T) {
	mock := &mockProvider{response: "```json\n{\"category\": \"architecture\", \"filename\": \"tool-calling-patterns\"}\n```"}
	loc, err := Categorize(context.Background(), mock, "tool calling architecture", []string{"llm", "architecture"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loc.Category != "architecture" {
		t.Errorf("category = %q, want %q", loc.Category, "architecture")
	}
	if loc.Filename != "tool-calling-patterns" {
		t.Errorf("filename = %q, want %q", loc.Filename, "tool-calling-patterns")
	}
}

func TestCategorize_NewCategory(t *testing.T) {
	mock := &mockProvider{response: `{"category": "security", "filename": "api-key-rotation"}`}
	loc, err := Categorize(context.Background(), mock, "API key rotation best practices", []string{"llm"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loc.Category != "security" {
		t.Errorf("category = %q, want %q", loc.Category, "security")
	}
}

func TestCategorize_Fallback(t *testing.T) {
	mock := &mockProvider{response: "I don't understand the question"}
	loc, err := Categorize(context.Background(), mock, "some topic here", []string{"llm"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loc.Category != "uncategorized" {
		t.Errorf("category = %q, want %q", loc.Category, "uncategorized")
	}
	if loc.Filename != "some-topic-here" {
		t.Errorf("filename = %q, want %q", loc.Filename, "some-topic-here")
	}
}

func TestCategorize_ProviderError(t *testing.T) {
	mock := &mockProvider{err: errMock}
	_, err := Categorize(context.Background(), mock, "test", []string{})
	if err == nil {
		t.Fatal("expected error from provider")
	}
}

func TestExistingCategories(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "llm"), 0755)
	os.Mkdir(filepath.Join(dir, "adblock"), 0755)
	os.Mkdir(filepath.Join(dir, ".hidden"), 0755)
	os.WriteFile(filepath.Join(dir, "stray-file.md"), []byte("hi"), 0644)

	cats, err := ExistingCategories(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cats) != 2 {
		t.Fatalf("expected 2 categories, got %d: %v", len(cats), cats)
	}
	// Results may be in any order depending on OS
	found := map[string]bool{}
	for _, c := range cats {
		found[c] = true
	}
	if !found["llm"] || !found["adblock"] {
		t.Errorf("expected llm and adblock, got %v", cats)
	}
}

func TestExistingCategories_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	cats, err := ExistingCategories(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cats) != 0 {
		t.Errorf("expected 0 categories, got %d", len(cats))
	}
}

func TestUniqueFilename_NoCollision(t *testing.T) {
	dir := t.TempDir()
	got := UniqueFilename(dir, "test-file")
	if got != "test-file" {
		t.Errorf("got %q, want %q", got, "test-file")
	}
}

func TestUniqueFilename_OneCollision(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test-file.md"), []byte("x"), 0644)
	got := UniqueFilename(dir, "test-file")
	if got != "test-file-2" {
		t.Errorf("got %q, want %q", got, "test-file-2")
	}
}

func TestUniqueFilename_TwoCollisions(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test-file.md"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "test-file-2.md"), []byte("x"), 0644)
	got := UniqueFilename(dir, "test-file")
	if got != "test-file-3" {
		t.Errorf("got %q, want %q", got, "test-file-3")
	}
}

func TestParseFileLocation_ValidJSON(t *testing.T) {
	loc, err := parseFileLocation(`{"category": "llm", "filename": "test"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loc.Category != "llm" || loc.Filename != "test" {
		t.Errorf("got %+v", loc)
	}
}

func TestParseFileLocation_EmptyCategory(t *testing.T) {
	_, err := parseFileLocation(`{"category": "", "filename": "test"}`)
	if err == nil {
		t.Fatal("expected error for empty category")
	}
}

func TestParseFileLocation_NoJSON(t *testing.T) {
	_, err := parseFileLocation("no json here at all")
	if err == nil {
		t.Fatal("expected error for no JSON")
	}
}
