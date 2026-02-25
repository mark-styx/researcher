package main

import (
	"strings"
	"testing"
)

func TestConfigShow(t *testing.T) {
	testSetup(t)

	out, err := runCmd(t, "config", "show")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Config show outputs YAML with key values
	if !strings.Contains(out, "default_backend") {
		t.Errorf("output missing 'default_backend': %s", out)
	}
	if !strings.Contains(out, "ollama") {
		t.Errorf("output missing 'ollama': %s", out)
	}
}

func TestConfigSet(t *testing.T) {
	testSetup(t)

	out, err := runCmd(t, "config", "set", "research_dir", "/tmp/new-research")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Set research_dir") {
		t.Errorf("output missing confirmation: %s", out)
	}

	// Read back and verify
	out2, err := runCmd(t, "config", "show")
	if err != nil {
		t.Fatalf("unexpected error on show: %v", err)
	}
	if !strings.Contains(out2, "/tmp/new-research") {
		t.Errorf("config show after set missing new value: %s", out2)
	}
}

func TestConfigSet_MissingArgs(t *testing.T) {
	testSetup(t)

	_, err := runCmd(t, "config", "set", "foo")
	if err == nil {
		t.Fatal("expected error for missing args")
	}
}
