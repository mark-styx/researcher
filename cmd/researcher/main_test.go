package main

import (
	"strings"
	"testing"
)

func TestRootCommand_HasSubcommands(t *testing.T) {
	root := buildRoot()

	expected := []string{
		"version", "init", "config", "ask", "dive",
		"list", "show", "search", "review", "enrich",
		"watch", "daemon", "schedule", "queue", "link",
	}

	cmds := root.Commands()
	names := make(map[string]bool)
	for _, c := range cmds {
		names[c.Name()] = true
	}

	for _, exp := range expected {
		if !names[exp] {
			t.Errorf("missing subcommand %q", exp)
		}
	}

	if len(cmds) != len(expected) {
		t.Errorf("got %d subcommands, want %d", len(cmds), len(expected))
	}
}

func TestVersionCommand(t *testing.T) {
	testSetup(t)

	out, err := runCmd(t, "version")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "researcher") {
		t.Errorf("output = %q, want to contain 'researcher'", out)
	}
}

func TestUnknownCommand(t *testing.T) {
	testSetup(t)

	_, err := runCmd(t, "bogus")
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
}
