package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
