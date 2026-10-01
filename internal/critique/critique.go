// Package critique holds the critic passes the hybrid backend runs over its
// aggregated answer, in a form any caller can run against any text and
// evidence: `researchguy critique`, the researchguy_critique MCP tool, and
// bookworm's chapter critique.
//
// Two flag-only critics:
//   - Groundedness. Lists claims the evidence does not support and changes
//     nothing. A flag means "not found in the evidence given", not "false".
//     The 2026-09-25 bake-off found rewrite mode deleting accurate facts that
//     were simply absent from the evidence, so rewrite mode is not exposed.
//   - Narrative vs. evidence. Lists claims presented as settled or consensus
//     that aren't tied to a distinct piece of evidence.
package critique

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// Evidence is one labeled piece of source material a text is checked
// against.
type Evidence struct {
	Label   string
	Content string
}

// Sentinels the critics write when they have nothing to report.
const (
	NoNarrativeClaims   = "No narrative-only claims found."
	NoUnsupportedClaims = "No unsupported claims found."
)

// GroundednessFlagSystemPrompt is the flag-mode groundedness critic: it
// lists unsupported claims and rewrites nothing.
func GroundednessFlagSystemPrompt() string {
	return "You are a groundedness critic. Read the text and the evidence it should rest on. " +
		"List every factual claim in the text (names, dates, numbers, events, attributions, causal claims) that the evidence does not support, or that overstates what the evidence says. " +
		"Do not rewrite the text. For each claim write one bullet: quote or closely paraphrase the claim, then in one line say what the evidence lacks or says instead. " +
		"Only judge against the evidence given: a claim can be true and still be flagged here, so never call a claim false on your own knowledge. " +
		"If every factual claim is supported, write exactly \"" + NoUnsupportedClaims + "\" Be dry and concise."
}

// BuildGroundednessFlagPrompt is the user prompt for the flag-mode critic.
func BuildGroundednessFlagPrompt(text string, evidence []Evidence) string {
	var b strings.Builder
	b.WriteString("Text to check:\n")
	b.WriteString(text)
	b.WriteString("\n\nEvidence:\n")
	writeEvidence(&b, evidence)
	return b.String()
}

// NarrativeSystemPrompt is the narrative-vs-evidence critic. It never
// rewrites.
func NarrativeSystemPrompt() string {
	return "You are a narrative-vs-evidence critic. Read the answer and the source evidence ledger it was built from. " +
		"For every claim in the answer presented as settled fact or consensus, check whether it is tied to a specific, distinct piece of evidence in the ledger, or whether it is a widely-repeated claim being restated without independent support. " +
		"List only the claims that lean narrative: quote or closely paraphrase the claim, then state in one line why it isn't distinctly evidenced. If every claim in the answer is directly evidenced, write exactly \"" + NoNarrativeClaims + "\" Be dry and concise. No prose padding, no restating the whole answer."
}

// BuildNarrativePrompt is the user prompt for the narrative critic.
func BuildNarrativePrompt(answer string, evidence []Evidence) string {
	var b strings.Builder
	b.WriteString("Answer to review:\n")
	b.WriteString(answer)
	b.WriteString("\n\nSource evidence ledger:\n")
	writeEvidence(&b, evidence)
	return b.String()
}

func writeEvidence(b *strings.Builder, evidence []Evidence) {
	for _, e := range evidence {
		if strings.TrimSpace(e.Content) == "" {
			continue
		}
		fmt.Fprintf(b, "\n[%s]\n", e.Label)
		b.WriteString(e.Content)
		b.WriteString("\n")
	}
}

// AppendNotes appends a visible "Critic Notes" section when either critic
// produced output, so findings are part of the saved document rather than
// only sitting in metadata.
func AppendNotes(final, groundedness, narrative string) string {
	groundedness = strings.TrimSpace(groundedness)
	narrative = strings.TrimSpace(narrative)
	if groundedness == "" && narrative == "" {
		return final
	}
	var b strings.Builder
	b.WriteString(final)
	b.WriteString("\n\n---\n\n## Critic Notes\n")
	if groundedness != "" {
		b.WriteString("\n### Groundedness Review\n\n")
		b.WriteString(groundedness)
		b.WriteString("\n")
	}
	if narrative != "" {
		b.WriteString("\n### Narrative vs. Evidence\n\n")
		b.WriteString(narrative)
		b.WriteString("\n")
	}
	return b.String()
}

// Completer runs one model call. critique doesn't import the llm package,
// which uses critique itself.
type Completer func(ctx context.Context, system, user string) (string, error)

// Result is a flag-mode critique.
type Result struct {
	// Groundedness lists claims the evidence does not support.
	Groundedness []string `json:"groundedness"`
	// Narrative lists claims restated as settled without distinct evidence.
	Narrative []string `json:"narrative"`
	Flags     int      `json:"flags"`
}

// Run critiques text against evidence in flag mode: nothing is rewritten.
func Run(ctx context.Context, complete Completer, text string, evidence []Evidence) (*Result, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("no text to critique")
	}
	grounded, err := complete(ctx, GroundednessFlagSystemPrompt(), BuildGroundednessFlagPrompt(text, evidence))
	if err != nil {
		return nil, fmt.Errorf("groundedness critic: %w", err)
	}
	narrative, err := complete(ctx, NarrativeSystemPrompt(), BuildNarrativePrompt(text, evidence))
	if err != nil {
		return nil, fmt.Errorf("narrative critic: %w", err)
	}
	r := &Result{
		Groundedness: ParseFlags(grounded, NoUnsupportedClaims),
		Narrative:    ParseFlags(narrative, NoNarrativeClaims),
	}
	r.Flags = len(r.Groundedness) + len(r.Narrative)
	return r, nil
}

var listItem = regexp.MustCompile(`^\s*(?:[-*+•]|\d+[.)])\s+(.*\S)`)

// ParseFlags turns a critic's list into items. With no list items, the none
// sentinel (or empty output) is no flags, and any other text becomes one
// item, so an off-format answer is surfaced rather than dropped.
func ParseFlags(raw, none string) []string {
	raw = strings.TrimSpace(raw)
	items := []string{}
	for _, line := range strings.Split(raw, "\n") {
		if m := listItem.FindStringSubmatch(line); m != nil {
			items = append(items, m[1])
		} else if len(items) > 0 && strings.TrimSpace(line) != "" && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			// Indented continuation of the previous item.
			items[len(items)-1] += " " + strings.TrimSpace(line)
		}
	}
	if len(items) > 0 || raw == "" || strings.Contains(raw, none) {
		return items
	}
	return []string{raw}
}

// Markdown renders a result for a feedback file.
func (r *Result) Markdown() string {
	var b strings.Builder
	section := func(title, note, none string, items []string) {
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", title, note)
		if len(items) == 0 {
			b.WriteString(none + "\n\n")
			return
		}
		for _, it := range items {
			b.WriteString("- " + it + "\n")
		}
		b.WriteString("\n")
	}
	section("Groundedness", "Claims the evidence given does not support. Not found in the evidence is not the same as false.", NoUnsupportedClaims, r.Groundedness)
	section("Narrative vs. evidence", "Claims stated as settled without a distinct piece of evidence behind them.", NoNarrativeClaims, r.Narrative)
	return strings.TrimRight(b.String(), "\n") + "\n"
}
