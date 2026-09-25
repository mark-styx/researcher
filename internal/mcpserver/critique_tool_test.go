package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/llm"
)

func TestCritique(t *testing.T) {
	cfg := testConfig(t)
	if err := os.WriteFile(filepath.Join(cfg.ResearchDir, "ev.md"), []byte("Founded 1951."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ResearchDir, "chapter.md"), []byte("Founded in 1950."), 0o644); err != nil {
		t.Fatal(err)
	}
	prov := &mockProvider{responses: []string{"- \"1950\": evidence says 1951", "No narrative-only claims found."}}

	res := call(t, cfg, prov, "researchguy_critique", map[string]any{
		"text_path":      "chapter.md",
		"evidence_paths": []any{"ev.md"},
	})
	var out map[string]any
	decode(t, res, &out)
	if out["flags"] != float64(1) || out["backend"] != "mock" || out["truncated"] != false {
		t.Fatalf("output = %v", out)
	}
	if g := out["groundedness"].([]any); len(g) != 1 {
		t.Errorf("groundedness = %v", g)
	}
	if len(prov.calls) != 2 || !strings.Contains(prov.calls[0].UserPrompt, "Founded in 1950.") || !strings.Contains(prov.calls[0].UserPrompt, "Founded 1951.") {
		t.Errorf("critic calls = %d; first prompt %q", len(prov.calls), prov.calls[0].UserPrompt)
	}

	// Inline text works too.
	prov2 := &mockProvider{response: "No unsupported claims found."}
	res = call(t, cfg, prov2, "researchguy_critique", map[string]any{"text": "inline", "evidence_paths": []any{"ev.md"}})
	decode(t, res, &out)
	if out["flags"] != float64(1) { // the narrative critic got the other sentinel, so it is one item
		t.Errorf("inline output = %v", out)
	}
}

func TestCritiqueErrors(t *testing.T) {
	cfg := testConfig(t)
	if err := os.WriteFile(filepath.Join(cfg.ResearchDir, "ev.md"), []byte("e"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]map[string]any{
		"no text":          {"evidence_paths": []any{"ev.md"}},
		"both text forms":  {"text": "a", "text_path": "ev.md", "evidence_paths": []any{"ev.md"}},
		"no evidence":      {"text": "a", "evidence_paths": []any{}},
		"evidence escapes": {"text": "a", "evidence_paths": []any{outside}},
		"text escapes":     {"text_path": outside, "evidence_paths": []any{"ev.md"}},
		"missing evidence": {"text": "a", "evidence_paths": []any{"none.md"}},
		"bad backend":      {"text": "a", "evidence_paths": []any{"ev.md"}, "backend": "gpt"},
	}
	for name, args := range cases {
		res := call(t, cfg, &mockProvider{response: "x"}, "researchguy_critique", args)
		if !res.IsError {
			t.Errorf("%s: want a tool error", name)
		}
	}

	// A backend override that can't be built is a tool error, not a crash.
	orig := newProvider
	t.Cleanup(func() { newProvider = orig })
	newProvider = func(*config.Config, string, string) (llm.Provider, error) { return nil, os.ErrNotExist }
	if res := call(t, cfg, &mockProvider{}, "researchguy_critique", map[string]any{"text": "a", "evidence_paths": []any{"ev.md"}, "backend": "claude"}); !res.IsError {
		t.Error("provider failure: want a tool error")
	}
}
