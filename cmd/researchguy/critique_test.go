package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCritiqueCmd(t *testing.T) {
	// The mock backend answers both critics with the same list.
	_, researchDir := testSetupWithOllama(t, "- \"founded in 1950\": evidence says 1951")
	text := filepath.Join(researchDir, "chapter.md")
	ev1 := filepath.Join(researchDir, "a.md")
	ev2 := filepath.Join(researchDir, "b.md")
	for path, content := range map[string]string{text: "It was founded in 1950.", ev1: "Founded 1951.", ev2: "More evidence."} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stdout, stderr, err := runCmdStdout(t, "critique", "--text", text, "--evidence", ev1, "--evidence", ev2, "--max-evidence-chars", "20", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("stdout is not JSON: %q", stdout)
	}
	if out["flags"] != float64(2) || out["truncated"] != true || out["evidence_chars"] != float64(20) || out["backend"] != "ollama" {
		t.Fatalf("output = %v", out)
	}
	if g := out["groundedness"].([]any); len(g) != 1 || !strings.Contains(g[0].(string), "evidence says 1951") {
		t.Errorf("groundedness = %v", g)
	}

	stdout, _, err = runCmdStdout(t, "critique", "--text", text, "--evidence", ev1)
	if err != nil || !strings.Contains(stdout, "## Groundedness") || !strings.Contains(stdout, "## Narrative vs. evidence") {
		t.Errorf("markdown output: %q, %v", stdout, err)
	}
}

func TestCritiqueCmdErrors(t *testing.T) {
	_, researchDir := testSetup(t)
	text := filepath.Join(researchDir, "chapter.md")
	if err := os.WriteFile(text, []byte("text"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"no evidence":      {"critique", "--text", text},
		"missing text":     {"critique", "--text", filepath.Join(researchDir, "none.md"), "--evidence", text},
		"missing evidence": {"critique", "--text", text, "--evidence", filepath.Join(researchDir, "none.md")},
		"bad backend":      {"critique", "--text", text, "--evidence", text, "--backend", "nope"},
	}
	for name, args := range cases {
		if _, _, err := runCmdStdout(t, args...); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
