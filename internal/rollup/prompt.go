package rollup

import "github.com/marklubin/researchguy/internal/graph"

const basePrompt = `You are updating a knowledge-graph node's summary because its linked
source document changed. Re-read the document below and produce a concise,
updated summary (2-4 sentences) that reflects its current content.

Do not invent claims the document doesn't support. Do not soften or remove
information that's present in the document. If the document's substance is
essentially unchanged, say so plainly rather than padding the summary.

Return only the summary text, no preamble or heading.`

const fundingPatternClause = `

## EXPLICIT NON-CLAIM

This node documents a funding-pattern observation: a funder and a funded
party, and what happened. Summarize only what the document establishes as
fact — who funded what, and what the funded work found or concluded. Do not
assert or imply the funder's intent, motive, or influence over the outcome.
Correlation between funding and favorable findings is not evidence of intent.

## SUFFICIENCY

State plainly whether the document's evidence is sufficient to support even
the non-claim summary above, or whether it's a single data point that
shouldn't be generalized. When in doubt, describe the observation narrowly
rather than broadly.`

// ResummarizationPrompt returns the system prompt for re-summarizing a node
// of the given type. funding-pattern nodes carry an additional clause
// preserving the non-claim/sufficiency framing the graph design requires for
// that node type; other node types don't carry that constraint. One shared
// template with a type-specific clause, not a full prompt per node type.
func ResummarizationPrompt(nodeType string) string {
	if nodeType == graph.NodeFundingPattern {
		return basePrompt + fundingPatternClause
	}
	return basePrompt
}
