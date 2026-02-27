package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testSetupWithOllama creates the test environment and starts a mock Ollama server.
// It writes a config pointing at the mock server's URL.
func testSetupWithOllama(t *testing.T, response string) (configDir, researchDir string) {
	t.Helper()

	configDir = t.TempDir()
	researchDir = t.TempDir()
	serverURL := ollamaServer(t, response)

	yaml := fmt.Sprintf(`research_dir: %s
default_backend: ollama
claude:
  binary: echo
  model: test
  max_tokens: 100
ollama:
  host: %s
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
`, researchDir, serverURL, configDir, configDir)

	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	t.Setenv("RESEARCHER_CONFIG_DIR", configDir)
	return configDir, researchDir
}

// --- ask ---

func TestAskCmd(t *testing.T) {
	testSetupWithOllama(t, "42")

	// Use --no-save --no-research to preserve original simple behavior
	out, err := runCmd(t, "ask", "What is the meaning of life?", "--no-save", "--no-research")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "42") {
		t.Errorf("output missing '42': %s", out)
	}
}

func TestAskCmd_WithSave(t *testing.T) {
	_, researchDir := testSetupWithOllama(t, "saved answer")

	out, err := runCmd(t, "ask", "test question", "--no-research")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "saved answer") {
		t.Errorf("output missing 'saved answer': %s", out)
	}
	if !strings.Contains(out, "Answer saved to:") {
		t.Errorf("output missing 'Answer saved to:': %s", out)
	}

	// Verify a file was actually created in the research dir
	found := false
	filepath.Walk(researchDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasSuffix(path, "-ask.md") {
			found = true
		}
		return nil
	})
	if !found {
		t.Error("expected an -ask.md file in research dir")
	}
}

func TestAskCmd_NoSaveFlag(t *testing.T) {
	testSetupWithOllama(t, "ephemeral answer")

	out, err := runCmd(t, "ask", "test question", "--no-save", "--no-research")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "ephemeral answer") {
		t.Errorf("output missing 'ephemeral answer': %s", out)
	}
	if strings.Contains(out, "Answer saved to:") {
		t.Error("should not show 'Answer saved to:' with --no-save")
	}
}

func TestAskCmd_MissingArg(t *testing.T) {
	testSetup(t)

	_, err := runCmd(t, "ask")
	if err == nil {
		t.Fatal("expected error for missing argument")
	}
}

// --- dive ---

func TestDiveCmd(t *testing.T) {
	testSetupWithOllama(t, "Deep dive content here")

	out, err := runCmd(t, "dive", "quantum computing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Research saved to") {
		t.Errorf("output missing 'Research saved to': %s", out)
	}
}

func TestDiveCmd_MissingArg(t *testing.T) {
	testSetup(t)

	_, err := runCmd(t, "dive")
	if err == nil {
		t.Fatal("expected error for missing argument")
	}
}

// --- review ---

func TestReviewCmd(t *testing.T) {
	testSetupWithOllama(t, "Review content here")

	out, err := runCmd(t, "review", "machine learning")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Review saved to") {
		t.Errorf("output missing 'Review saved to': %s", out)
	}
}

func TestReviewCmd_WithSources(t *testing.T) {
	_, researchDir := testSetupWithOllama(t, "Review with sources")

	// Create a temp source file
	srcFile := filepath.Join(researchDir, "source.md")
	os.WriteFile(srcFile, []byte("# Source Document\nSome content."), 0644)

	_, err := runCmd(t, "review", "test topic", "--sources", srcFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- enrich ---

func TestEnrichCmd(t *testing.T) {
	_, researchDir := testSetupWithOllama(t, "Enriched content here")

	// Create a temp document to enrich
	docFile := filepath.Join(researchDir, "doc.md")
	os.WriteFile(docFile, []byte("# Original Document\nNeeds enrichment."), 0644)

	out, err := runCmd(t, "enrich", docFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Enriched document") {
		t.Errorf("output missing 'Enriched document': %s", out)
	}

	// Verify enriched file was created
	enrichedFile := filepath.Join(researchDir, "doc-enriched.md")
	if _, err := os.Stat(enrichedFile); err != nil {
		t.Errorf("enriched file not created: %v", err)
	}
}

func TestEnrichCmd_MissingArg(t *testing.T) {
	testSetup(t)

	_, err := runCmd(t, "enrich")
	if err == nil {
		t.Fatal("expected error for missing argument")
	}
}
