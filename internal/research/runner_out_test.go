package research

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunner_OutPathSkipsCategorizer(t *testing.T) {
	for _, tc := range []struct {
		typ  string
		task Task
	}{
		{TypeDive, Task{Type: TypeDive, Topic: "t"}},
		{TypeReview, Task{Type: TypeReview, Topic: "t"}},
		{TypeCompare, Task{Type: TypeCompare, Topic: "a vs b"}},
		{TypeAsk, Task{Type: TypeAsk, Topic: "q", Quiet: true}},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			cfg := testConfig(t)
			mock := &mockProvider{response: "body"}
			out := filepath.Join(t.TempDir(), "nested", "exact.md")
			task := tc.task
			task.OutPath = out
			task.NoResearch = true

			res, err := NewRunner(cfg, mock).Run(context.Background(), task)
			if err != nil {
				t.Fatal(err)
			}
			if res.FilePath != out {
				t.Fatalf("FilePath = %q, want %q", res.FilePath, out)
			}
			data, err := os.ReadFile(out)
			if err != nil || !strings.Contains(string(data), "body") {
				t.Fatalf("output not written: %v", err)
			}
			if len(mock.calls) != 1 {
				t.Fatalf("got %d LLM calls, want 1 (no categorizer)", len(mock.calls))
			}
		})
	}
}

func TestRunner_OutPathUnwritable(t *testing.T) {
	cfg := testConfig(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewRunner(cfg, &mockProvider{response: "x"}).Run(context.Background(),
		Task{Type: TypeDive, Topic: "t", NoResearch: true, OutPath: filepath.Join(blocker, "sub", "x.md")})
	if err == nil || !strings.Contains(err.Error(), "creating output dir") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunner_ProjectsReachGrepai(t *testing.T) {
	cfg := testConfig(t)
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := filepath.Join(dir, "fake-grepai")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" > "+argsFile+"\necho '[]'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Grepai.Binary = script
	cfg.Grepai.Workspace = "ws"
	cfg.Grepai.Project = "research"

	_, err := NewRunner(cfg, &mockProvider{response: "x"}).Run(context.Background(),
		Task{Type: TypeAsk, Topic: "q", Quiet: true, NoSave: true, Projects: []string{"book_a", "book_b"}})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--project book_a --project book_b") || strings.Contains(string(args), "--project research") {
		t.Fatalf("grepai args = %s", args)
	}
}
