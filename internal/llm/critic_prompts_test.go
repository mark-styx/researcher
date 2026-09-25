package llm

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/marklubin/researchguy/internal/critique"
)

// The critic prompts moved to internal/critique. The old_ functions below are
// the pre-move code, copied verbatim, so the hybrid backend's prompts are
// checked to be byte-for-byte unchanged.

func old_groundednessCriticSystemPrompt() string {
	return "You are a groundedness critic. Keep only claims supported by worker evidence, fix unsupported or overstated claims, and keep the final answer concise and accurate. " +
		"Never silently edit: after the revised answer, on its own line write exactly \"" + critique.ChangesMarker + "\", then a bullet list of every claim you removed, softened, or flagged as unsupported, each with a one-line reason. If you changed nothing, write \"No changes.\" after the marker."
}

func old_buildVerificationPrompt(original, draft string, workers []hybridWorkerOutput) string {
	var b strings.Builder
	b.WriteString("Original request:\n")
	b.WriteString(original)
	b.WriteString("\n\nDraft answer:\n")
	b.WriteString(draft)
	b.WriteString("\n\nWorker evidence:\n")
	for _, w := range workers {
		if w.Err != nil || strings.TrimSpace(w.Content) == "" {
			continue
		}
		b.WriteString(fmt.Sprintf("\n[%s/%s | shard: %s]\n", w.Backend, w.Model, w.Shard))
		b.WriteString(w.Content)
		b.WriteString("\n")
	}
	b.WriteString("\nRevise the draft so every strong claim is traceable to worker evidence or clearly marked as uncertain.")
	return b.String()
}

func old_narrativeCriticSystemPrompt() string {
	return "You are a narrative-vs-evidence critic. Read the answer and the worker evidence it was built from. " +
		"For every claim in the answer presented as settled fact or consensus, check whether it is tied to a specific, distinct piece of evidence in the worker outputs, or whether it is a widely-repeated claim being restated without independent support. " +
		"List only the claims that lean narrative: quote or closely paraphrase the claim, then state in one line why it isn't distinctly evidenced. If every claim in the answer is directly evidenced, write exactly \"No narrative-only claims found.\" Be dry and concise. No prose padding, no restating the whole answer."
}

func old_buildNarrativeCritiquePrompt(answer string, workers []hybridWorkerOutput) string {
	var b strings.Builder
	b.WriteString("Answer to review:\n")
	b.WriteString(answer)
	b.WriteString("\n\nWorker evidence it was built from:\n")
	for _, w := range workers {
		if w.Err != nil || strings.TrimSpace(w.Content) == "" {
			continue
		}
		b.WriteString(fmt.Sprintf("\n[%s/%s | shard: %s]\n", w.Backend, w.Model, w.Shard))
		b.WriteString(w.Content)
		b.WriteString("\n")
	}
	return b.String()
}

func old_appendCriticNotes(final, groundednessChanges, narrativeFlags string) string {
	groundednessChanges = strings.TrimSpace(groundednessChanges)
	narrativeFlags = strings.TrimSpace(narrativeFlags)
	if groundednessChanges == "" && narrativeFlags == "" {
		return final
	}
	var b strings.Builder
	b.WriteString(final)
	b.WriteString("\n\n---\n\n## Critic Notes\n")
	if groundednessChanges != "" {
		b.WriteString("\n### Groundedness Review\n\n")
		b.WriteString(groundednessChanges)
		b.WriteString("\n")
	}
	if narrativeFlags != "" {
		b.WriteString("\n### Narrative vs. Evidence\n\n")
		b.WriteString(narrativeFlags)
		b.WriteString("\n")
	}
	return b.String()
}

func TestCriticPromptsUnchangedByMove(t *testing.T) {
	workers := []hybridWorkerOutput{
		{Backend: "ollama", Model: "m1", Shard: "primary evidence", Content: "worker one says X (2019)."},
		{Backend: "ollama", Model: "m2", Shard: "counter-evidence", Err: errors.New("timeout"), Content: "partial"},
		{Backend: "claude", Model: "sonnet", Shard: "empty", Content: "  "},
		{Backend: "claude", Model: "sonnet", Shard: "history", Content: "worker four says Y."},
	}
	ev := workerEvidence(workers)
	checks := []struct{ name, got, want string }{
		{"groundedness system", critique.GroundednessRewriteSystemPrompt(), old_groundednessCriticSystemPrompt()},
		{"groundedness user", critique.BuildGroundednessRewritePrompt("the question", "the draft", ev), old_buildVerificationPrompt("the question", "the draft", workers)},
		{"narrative system", critique.NarrativeSystemPrompt(), old_narrativeCriticSystemPrompt()},
		{"narrative user", critique.BuildNarrativePrompt("the answer", ev), old_buildNarrativeCritiquePrompt("the answer", workers)},
		{"notes", critique.AppendNotes("final", "- g", "- n"), old_appendCriticNotes("final", "- g", "- n")},
		{"no notes", critique.AppendNotes("final", " ", ""), old_appendCriticNotes("final", " ", "")},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s changed:\n got %q\nwant %q", c.name, c.got, c.want)
		}
	}
	if strings.Contains(critique.BuildNarrativePrompt("a", ev), "partial") {
		t.Error("an errored worker's content reached the critic")
	}
}
