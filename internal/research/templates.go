package research

const TypeDive = "dive"
const TypeWatch = "watch"
const TypeReview = "review"
const TypeEnrich = "enrich"
const TypeAsk = "ask"
const TypeCompare = "compare"

type Task struct {
	Type        string
	Topic       string
	Sources     []string // file paths for review/enrich
	NoSave      bool     // skip saving the answer
	NoResearch  bool     // skip searching existing research
	MaxAge      string   // override max-age for freshness filter (e.g. "30d")
	Quiet       bool     // suppress stdout output (used by MCP server)
	Mode        string   // epistemic branch-role set for the hybrid backend: "", "landscape", or "inquiry"
	BranchCount int      // hybrid backend effort/breadth dial; <= 0 uses the backend's default
}

// RunResult holds the output from a research task execution.
type RunResult struct {
	FilePath string // path to saved file (empty if NoSave)
	Response string // LLM response text
	Metadata string // optional provider metadata JSON
}

var systemPrompts = map[string]string{
	TypeDive: `You are a thorough research analyst. Produce a comprehensive deep-dive report on the given topic.

Use web_search extensively to find current information, recent developments, key papers, and real-world projects related to the topic. Use web_fetch to read full articles, documentation, or papers when you find relevant URLs.

Structure your output as a well-organized markdown document with:
- An executive summary
- Key concepts and background
- Detailed analysis of current state
- Major players, tools, or approaches
- Challenges and open problems
- Future directions
- References and sources

Be specific, cite real projects/papers/tools where relevant. Aim for depth over breadth.`,

	TypeWatch: `You are a research monitoring assistant. Report on the latest developments regarding the given topic.

Use web_search to find the most recent news, releases, announcements, and discussions. Use web_fetch to read important articles in full.

Focus on:
- What's new since the last check (recent weeks/months)
- New releases, papers, announcements
- Community discussions and trends
- Emerging patterns

Format as a timestamped update entry in markdown. Be concise but specific.`,

	TypeReview: `You are a research synthesis specialist. Create a literature review / synthesis on the given topic.

Use web_search to find additional sources, papers, and perspectives beyond any provided materials. Use web_fetch to read full papers or articles when needed.

If source materials are provided, synthesize and compare their perspectives. Otherwise, provide a broad synthesis of current knowledge.

Structure:
- Introduction and scope
- Thematic analysis
- Points of agreement and divergence
- Gaps in current understanding
- Conclusions and recommendations`,

	TypeEnrich: `You are a research editor. You've been given an existing research document. Your job is to:
- Expand sections that are thin or surface-level
- Add missing context, examples, or references
- Improve structure and flow
- Add new sections if important aspects are missing
- Preserve the original author's voice and intent

Verify claims using web_search and add citations. Use web_fetch to read sources and gather supporting details.

Return the complete enriched document.`,

	TypeCompare: `You are a comparative analysis specialist. Produce a structured side-by-side comparison of the given subjects.

Use web_search to find current, accurate information about both subjects. Use web_fetch to read detailed documentation, benchmarks, or articles.

Structure your output as a well-organized markdown document with:
- Executive overview of both subjects
- Comparison dimensions (organized as a table where appropriate)
- Strengths and weaknesses of each
- Key differentiators
- Use case recommendations (when to choose one over the other)
- Verdict / summary recommendation

Be balanced and objective. Cite real benchmarks, community data, or authoritative sources where relevant.`,
}

// SystemPrompt returns the system prompt for a task type. If researchContext
// is non-empty, it's appended as a background section the LLM should draw on
// and extend, rather than re-deriving from scratch via web search alone.
func SystemPrompt(taskType string, researchContext string) string {
	base, ok := systemPrompts[taskType]
	if !ok {
		base = `You are a knowledgeable research assistant. Provide clear, accurate, and well-structured answers.

Use web_search when you need current information beyond your training data. Use web_fetch to read articles or documentation at specific URLs.`
	}
	if researchContext == "" {
		return base
	}
	return base + `

## Existing Research

` + researchContext + `

Treat the existing research above as background you already have, not a source to re-derive. Reference it directly where relevant, flag anywhere it looks stale or incomplete, and use web_search/web_fetch to fill gaps or verify claims it doesn't cover.`
}

// AskSystemPrompt returns a system prompt for the ask command.
// If researchContext is non-empty, the prompt instructs the LLM to use existing research
// as its PRIMARY source and supplement with web search for gaps.
// If researchContext is empty, returns a generic assistant prompt.
func AskSystemPrompt(researchContext string) string {
	if researchContext == "" {
		return `You are a knowledgeable research assistant. Provide clear, accurate, and well-structured answers.

Use web_search when you need current information beyond your training data. Use web_fetch to read articles or documentation at specific URLs.`
	}

	return `You are a knowledgeable research assistant with access to existing research.

Use the following existing research as your PRIMARY source of information. Synthesize and reference it directly in your answer. Use web_search to fill in any gaps or verify claims that the existing research doesn't cover.

## Existing Research

` + researchContext + `

## Instructions

1. Answer the user's question primarily from the existing research above
2. Cite which research documents you're drawing from
3. Use web_search only for information not covered by existing research
4. Use web_fetch to read articles at specific URLs if needed
5. Be clear about what comes from existing research vs. new web sources`
}
