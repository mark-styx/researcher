package llm

import (
	"errors"
	"strings"
	"testing"

	"github.com/marklubin/researchguy/internal/critique"
)

func TestWorkerEvidenceUsesLedgerNotWorkerProse(t *testing.T) {
	workers := []hybridWorkerOutput{
		{
			Backend: "ollama",
			Model:   "m1",
			Shard:   "primary evidence",
			Content: "worker interpretation",
			Evidence: []critique.Evidence{{
				Label:   "ollama/m1 | web_fetch source",
				Content: "raw source text",
			}},
		},
		{
			Backend:  "ollama",
			Model:    "m2",
			Shard:    "counter-evidence",
			Err:      errors.New("timeout"),
			Evidence: []critique.Evidence{{Label: "partial", Content: "must not leak"}},
		},
	}

	evidence := workerEvidence(workers)
	if len(evidence) != 1 || evidence[0].Content != "raw source text" {
		t.Fatalf("workerEvidence() = %+v", evidence)
	}
	prompt := critique.BuildGroundednessFlagPrompt("draft", evidence)
	if strings.Contains(prompt, "worker interpretation") || strings.Contains(prompt, "must not leak") {
		t.Errorf("non-evidence content reached critic: %s", prompt)
	}
	if !strings.Contains(prompt, "raw source text") {
		t.Errorf("raw evidence missing from critic: %s", prompt)
	}
}

func TestCriticNotesRemainVisibleWithoutRewriting(t *testing.T) {
	got := critique.AppendNotes("final", "- unsupported", "- narrative")
	if !strings.HasPrefix(got, "final") {
		t.Fatalf("answer body changed: %q", got)
	}
	if !strings.Contains(got, "### Groundedness Review\n\n- unsupported") || !strings.Contains(got, "### Narrative vs. Evidence\n\n- narrative") {
		t.Errorf("critic notes missing: %s", got)
	}
}
