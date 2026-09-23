package llm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marklubin/researchguy/internal/tools"
)

func TestClaude_Name(t *testing.T) {
	c := &Claude{}
	if got := c.Name(); got != "claude" {
		t.Errorf("Name() = %q, want %q", got, "claude")
	}
}

func TestClaude_Complete(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude")
	err := os.WriteFile(script, []byte("#!/bin/sh\necho 'test response'\n"), 0755)
	if err != nil {
		t.Fatalf("writing fake script: %v", err)
	}

	c := &Claude{Binary: script, Model: "test", MaxTokens: 100}
	got, err := c.Complete(context.Background(), Request{UserPrompt: "hello"})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got != "test response" {
		t.Errorf("Complete() = %q, want %q", got, "test response")
	}
}

func TestClaude_Complete_WithSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude")
	// Script that echoes all args so we can verify system prompt is passed
	err := os.WriteFile(script, []byte("#!/bin/sh\necho 'with system'\n"), 0755)
	if err != nil {
		t.Fatalf("writing fake script: %v", err)
	}

	c := &Claude{Binary: script, Model: "test", MaxTokens: 100}
	got, err := c.Complete(context.Background(), Request{
		SystemPrompt: "You are a helper",
		UserPrompt:   "hello",
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got != "with system" {
		t.Errorf("Complete() = %q, want %q", got, "with system")
	}
}

func TestClaude_Complete_WithTools(t *testing.T) {
	dir := t.TempDir()
	// Script that prints args to stderr and response to stdout
	script := filepath.Join(dir, "fake-claude")
	err := os.WriteFile(script, []byte("#!/bin/sh\necho 'tool response'\n"), 0755)
	if err != nil {
		t.Fatalf("writing fake script: %v", err)
	}

	c := &Claude{Binary: script, Model: "test", MaxTokens: 100}
	got, err := c.Complete(context.Background(), Request{
		UserPrompt: "search for something",
		Tools: []tools.Tool{
			{Name: "web_search", Description: "Search the web"},
			{Name: "web_fetch", Description: "Fetch a URL"},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got != "tool response" {
		t.Errorf("Complete() = %q, want %q", got, "tool response")
	}
}

func TestClaude_Complete_WithMaxBudget(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude")
	// Script that echoes args so we can verify --max-budget-usd is passed
	err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\"\n"), 0755)
	if err != nil {
		t.Fatalf("writing fake script: %v", err)
	}

	c := &Claude{Binary: script, Model: "test", MaxBudgetUSD: 1.50}
	got, err := c.Complete(context.Background(), Request{
		UserPrompt: "test",
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if !strings.Contains(got, "--max-budget-usd") {
		t.Errorf("output %q should contain --max-budget-usd", got)
	}
	if !strings.Contains(got, "1.50") {
		t.Errorf("output %q should contain budget value 1.50", got)
	}
}

func TestClaude_Complete_BinaryNotFound(t *testing.T) {
	c := &Claude{Binary: "/nonexistent/binary", Model: "test", MaxTokens: 100}
	_, err := c.Complete(context.Background(), Request{UserPrompt: "hello"})
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}
