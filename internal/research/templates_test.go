package research

import (
	"strings"
	"testing"
)

func TestSystemPrompt_KnownTypes(t *testing.T) {
	types := []string{TypeDive, TypeWatch, TypeReview, TypeEnrich}
	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			p := SystemPrompt(typ)
			if p == "" {
				t.Error("expected non-empty prompt")
			}
			if strings.Contains(p, "knowledgeable research assistant") {
				t.Error("got fallback prompt for known type")
			}
		})
	}
}

func TestSystemPrompt_Ask(t *testing.T) {
	p := SystemPrompt(TypeAsk)
	if p == "" {
		t.Error("expected non-empty prompt")
	}
	// TypeAsk is not in the map, so it should return the fallback
	if !strings.Contains(p, "knowledgeable research assistant") {
		t.Error("expected fallback prompt for ask type")
	}
}

func TestSystemPrompt_Unknown(t *testing.T) {
	p := SystemPrompt("bogus")
	if !strings.Contains(p, "knowledgeable research assistant") {
		t.Error("expected fallback prompt for unknown type")
	}
}

func TestAskSystemPrompt_NoContext(t *testing.T) {
	p := AskSystemPrompt("")
	if p == "" {
		t.Error("expected non-empty prompt")
	}
	if !strings.Contains(p, "knowledgeable research assistant") {
		t.Error("expected generic prompt when no context")
	}
	if strings.Contains(p, "Existing Research") {
		t.Error("should not contain 'Existing Research' section when context is empty")
	}
}

func TestAskSystemPrompt_WithContext(t *testing.T) {
	ctx := "--- Source: agents.md ---\nSome research about agents\n"
	p := AskSystemPrompt(ctx)
	if p == "" {
		t.Error("expected non-empty prompt")
	}
	if !strings.Contains(p, "Existing Research") {
		t.Error("expected 'Existing Research' section")
	}
	if !strings.Contains(p, "PRIMARY source") {
		t.Error("expected 'PRIMARY source' instruction")
	}
	if !strings.Contains(p, "agents.md") {
		t.Error("expected research context to be included")
	}
}

func TestSystemPrompt_ContainsToolInstructions(t *testing.T) {
	types := []string{TypeDive, TypeWatch, TypeReview, TypeEnrich}
	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			p := SystemPrompt(typ)
			if !strings.Contains(p, "web_search") {
				t.Error("expected prompt to mention web_search")
			}
			if !strings.Contains(p, "web_fetch") {
				t.Error("expected prompt to mention web_fetch")
			}
		})
	}
}

func TestTaskTypeConstants(t *testing.T) {
	if TypeDive != "dive" {
		t.Errorf("TypeDive = %q", TypeDive)
	}
	if TypeWatch != "watch" {
		t.Errorf("TypeWatch = %q", TypeWatch)
	}
	if TypeReview != "review" {
		t.Errorf("TypeReview = %q", TypeReview)
	}
	if TypeEnrich != "enrich" {
		t.Errorf("TypeEnrich = %q", TypeEnrich)
	}
	if TypeAsk != "ask" {
		t.Errorf("TypeAsk = %q", TypeAsk)
	}
}
