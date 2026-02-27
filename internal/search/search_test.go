package search

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marklubin/researcher/internal/config"
)

func TestQuery_MissingBinary(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		ResearchDir: dir,
		Grepai: config.GrepaiConfig{
			Binary: "nonexistent-binary-xyz",
		},
	}

	_, err := Query(cfg, "test query", 5)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "grepai search failed") {
		t.Errorf("error = %q, expected to contain 'grepai search failed'", err.Error())
	}
}

func TestQuery_WithEchoBinary(t *testing.T) {
	dir := t.TempDir()

	// Create a fake grepai script that echoes its args
	script := filepath.Join(dir, "fake-grepai")
	err := os.WriteFile(script, []byte("#!/bin/sh\necho \"args: $*\"\n"), 0755)
	if err != nil {
		t.Fatalf("writing script: %v", err)
	}

	cfg := &config.Config{
		ResearchDir: dir,
		Grepai: config.GrepaiConfig{
			Binary: script,
		},
	}

	out, err := Query(cfg, "my query", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "search") {
		t.Errorf("output = %q, expected to contain 'search'", out)
	}
	if !strings.Contains(out, "my query") {
		t.Errorf("output = %q, expected to contain 'my query'", out)
	}
	if !strings.Contains(out, "--limit") {
		t.Errorf("output = %q, expected to contain '--limit'", out)
	}
	if !strings.Contains(out, "3") {
		t.Errorf("output = %q, expected to contain '3'", out)
	}
}

func TestQueryJSON(t *testing.T) {
	dir := t.TempDir()
	researchDir := t.TempDir()

	results := []SearchResult{
		{FilePath: "llm/agents.md", StartLine: 1, EndLine: 10, Score: 0.95, Content: "LLM agents overview"},
		{FilePath: "llm/tools.md", StartLine: 5, EndLine: 15, Score: 0.80, Content: "Tool use patterns"},
	}
	jsonBytes, _ := json.Marshal(results)

	script := filepath.Join(dir, "fake-grepai")
	err := os.WriteFile(script, []byte("#!/bin/sh\ncat <<'JSONEOF'\n"+string(jsonBytes)+"\nJSONEOF\n"), 0755)
	if err != nil {
		t.Fatalf("writing fake script: %v", err)
	}

	cfg := &config.Config{
		ResearchDir: researchDir,
		Grepai:      config.GrepaiConfig{Binary: script},
	}

	got, err := QueryJSON(cfg, "LLM agents", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	if got[0].FilePath != "llm/agents.md" {
		t.Errorf("first result path = %q, want %q", got[0].FilePath, "llm/agents.md")
	}
	if got[1].Score != 0.80 {
		t.Errorf("second result score = %v, want 0.80", got[1].Score)
	}
}

func TestQueryJSON_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	researchDir := t.TempDir()

	script := filepath.Join(dir, "fake-grepai")
	err := os.WriteFile(script, []byte("#!/bin/sh\necho 'not json'\n"), 0755)
	if err != nil {
		t.Fatalf("writing fake script: %v", err)
	}

	cfg := &config.Config{
		ResearchDir: researchDir,
		Grepai:      config.GrepaiConfig{Binary: script},
	}

	_, err = QueryJSON(cfg, "test", 5)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseMaxAge(t *testing.T) {
	tests := []struct {
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"90d", 90 * 24 * time.Hour, false},
		{"2w", 2 * 7 * 24 * time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"", 0, true},
		{"abc", 0, true},
		{"0d", 0, true},
		{"-1d", 0, true},
		{"10x", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseMaxAge(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseMaxAge(%q) = %v, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMaxAge(%q) error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ParseMaxAge(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestFilterFresh(t *testing.T) {
	researchDir := t.TempDir()

	// Create a "fresh" file (just created, mod time = now)
	freshPath := filepath.Join(researchDir, "fresh.md")
	os.WriteFile(freshPath, []byte("fresh content"), 0644)

	// Create an "old" file and set its mod time to 200 days ago
	oldPath := filepath.Join(researchDir, "old.md")
	os.WriteFile(oldPath, []byte("old content"), 0644)
	oldTime := time.Now().Add(-200 * 24 * time.Hour)
	os.Chtimes(oldPath, oldTime, oldTime)

	results := []SearchResult{
		{FilePath: "fresh.md", Score: 0.9},
		{FilePath: "old.md", Score: 0.8},
	}

	maxAge := 90 * 24 * time.Hour
	fresh := FilterFresh(results, researchDir, maxAge)

	if len(fresh) != 1 {
		t.Fatalf("expected 1 fresh result, got %d", len(fresh))
	}
	if fresh[0].FilePath != "fresh.md" {
		t.Errorf("expected fresh.md, got %q", fresh[0].FilePath)
	}
}

func TestFilterFresh_MissingFile(t *testing.T) {
	researchDir := t.TempDir()

	results := []SearchResult{
		{FilePath: "nonexistent.md", Score: 0.9},
	}

	fresh := FilterFresh(results, researchDir, 90*24*time.Hour)
	if len(fresh) != 0 {
		t.Errorf("expected 0 results for missing file, got %d", len(fresh))
	}
}

func TestReadContents(t *testing.T) {
	researchDir := t.TempDir()

	os.WriteFile(filepath.Join(researchDir, "a.md"), []byte("content A"), 0644)
	os.WriteFile(filepath.Join(researchDir, "b.md"), []byte("content B"), 0644)

	results := []SearchResult{
		{FilePath: "a.md", Score: 0.9},
		{FilePath: "a.md", Score: 0.7}, // duplicate
		{FilePath: "b.md", Score: 0.8},
		{FilePath: "missing.md", Score: 0.5}, // missing file
	}

	contents := ReadContents(results, researchDir)

	if len(contents) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(contents))
	}

	pathA := filepath.Join(researchDir, "a.md")
	pathB := filepath.Join(researchDir, "b.md")

	if contents[pathA] != "content A" {
		t.Errorf("content A = %q", contents[pathA])
	}
	if contents[pathB] != "content B" {
		t.Errorf("content B = %q", contents[pathB])
	}
}

func TestFormatContext_Empty(t *testing.T) {
	result := FormatContext(nil)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestFormatContext_WithContents(t *testing.T) {
	contents := map[string]string{
		"/research/llm/agents.md": "Agents content",
		"/research/llm/tools.md":  "Tools content",
	}
	result := FormatContext(contents)
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	if !strings.Contains(result, "agents.md") || !strings.Contains(result, "tools.md") {
		t.Errorf("missing filenames in result: %s", result)
	}
	if !strings.Contains(result, "Agents content") || !strings.Contains(result, "Tools content") {
		t.Errorf("missing content in result: %s", result)
	}
}
