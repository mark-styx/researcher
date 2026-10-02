package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/embed"
	"github.com/marklubin/researchguy/internal/fetch"
	"github.com/marklubin/researchguy/internal/store/retrieve"
)

// storeTools serve the store index. The index is opened on the first call
// that needs it, so the server starts without Postgres and a later call
// retries a failed open.
type storeTools struct {
	cfg *config.Config

	mu sync.Mutex
	r  *retrieve.Retriever
}

func (t *storeTools) retriever(ctx context.Context) (*retrieve.Retriever, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.r != nil {
		return t.r, nil
	}
	if strings.TrimSpace(t.cfg.Store.DSN) == "" {
		return nil, errors.New("store.dsn is not set, so there is no index of fetched documents; researchguy_context searches report files")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, err := retrieve.Open(ctx, t.cfg)
	if err != nil {
		return nil, fmt.Errorf("opening the store index: %w", err)
	}
	t.r = r
	return r, nil
}

// addStoreTools registers the retrieval tools, and with write the tool
// that fetches into the store.
func addStoreTools(s *server.MCPServer, cfg *config.Config, write bool) {
	t := &storeTools{cfg: cfg}
	s.AddTool(findTool(), t.findHandler)
	s.AddTool(passageTool(), t.passageHandler)
	s.AddTool(documentTool(), t.documentHandler)
	s.AddTool(sourceTool(), t.sourceHandler)
	if write {
		s.AddTool(ingestURLTool(), t.ingestURLHandler)
	}
}

// parseRefID reads an id given as a number or as its ref (P:123, S:123).
// IDs are 63-bit, past what a JSON number holds exactly, so tools take
// them as strings.
func parseRefID(s, prefix string) (int64, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), prefix+":")
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid id %q", s)
	}
	return id, nil
}

// lookupError turns a lookup failure into a tool error.
func lookupError(err error) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultError(err.Error()), nil
}

// --- researchguy_find ---

func findTool() mcp.Tool {
	return mcp.NewTool("researchguy_find",
		mcp.WithDescription("Search the passages of every document researchguy fetched and of its own reports, by full-text match and by meaning, fused. "+
			"Each card has its source URL and domain, the publication date when the source states one (with where the date came from, and weak when it's a guess), "+
			"when it was collected, and kind: passage (primary evidence from a fetched document; content_kind abstract means only the abstract was fetched) or "+
			"report (researchguy's own earlier synthesis, never primary evidence). Nothing is dropped for age unless you filter. "+
			"Cite a card as [P:<id>] using its ref. mode full_text with a note means the embedding model was down. No LLM call."),
		mcp.WithString("query", mcp.Required(), mcp.Description("What to find, in plain words")),
		mcp.WithArray("kinds", mcp.Description("passage, report, or both (default both)"), mcp.WithStringEnumItems([]string{retrieve.KindPassage, retrieve.KindReport})),
		mcp.WithString("since", mcp.Description("Earliest date: YYYY, YYYY-MM, YYYY-MM-DD or an age such as 2y. Bounds the publication date unless date_field is collected; leaves out undated documents")),
		mcp.WithString("until", mcp.Description("Latest date, inclusive (until 2020 takes in all of 2020)")),
		mcp.WithString("date_field", mcp.Description("Date since/until bound"), mcp.Enum(retrieve.DatePublished, retrieve.DateCollected)),
		mcp.WithString("as_of", mcp.Description("Only what had been collected by this date: what an earlier report could have seen")),
		mcp.WithString("run_id", mcp.Description("Only documents this run fetched, and its report")),
		mcp.WithString("domain", mcp.Description("Only this domain and its subdomains (e.g. nih.gov)")),
		mcp.WithString("prefer_recent", mcp.Description("Halve a card's score per this much age (e.g. 1y). Off by default: an old primary source isn't worse for being old")),
		mcp.WithNumber("min_similarity", mcp.Description("Drop meaning matches less similar than this (0-1). Default 0 keeps the nearest, with their similarity")),
		mcp.WithNumber("limit", mcp.Description("Cards to return (default 10, at most 100)")),
	)
}

func (t *storeTools) findHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	text, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	q, err := retrieve.Args{
		Kinds: req.GetStringSlice("kinds", nil), Since: req.GetString("since", ""), Until: req.GetString("until", ""),
		DateField: req.GetString("date_field", ""), AsOf: req.GetString("as_of", ""), RunID: req.GetString("run_id", ""),
		Domain: req.GetString("domain", ""), PreferRecent: req.GetString("prefer_recent", ""),
		MinSimilarity: req.GetFloat("min_similarity", 0), Limit: req.GetInt("limit", 10),
	}.Query(text, time.Now())
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	r, err := t.retriever(ctx)
	if err != nil {
		return lookupError(err)
	}
	res, err := r.Find(ctx, q)
	if err != nil {
		return lookupError(err)
	}
	return toolResultJSON(res)
}

// --- researchguy_passage ---

func passageTool() mcp.Tool {
	return mcp.NewTool("researchguy_passage",
		mcp.WithDescription("One indexed passage as a card (source, dates, kind) with the passages around it, for checking a [P:<id>] citation in context."),
		mcp.WithString("id", mcp.Required(), mcp.Description("The passage ref (P:123) or id")),
		mcp.WithNumber("neighbors", mcp.Description("Passages to include on each side (default 1, at most 5)")),
	)
}

func (t *storeTools) passageHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw, err := req.RequireString("id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	id, err := parseRefID(raw, retrieve.RefPassage)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	r, err := t.retriever(ctx)
	if err != nil {
		return lookupError(err)
	}
	p, err := r.Passage(ctx, id, req.GetInt("neighbors", 1))
	if err != nil {
		return lookupError(err)
	}
	return toolResultJSON(p)
}

// --- researchguy_document ---

func documentTool() mcp.Tool {
	return mcp.NewTool("researchguy_document",
		mcp.WithDescription(fmt.Sprintf("A fetched document's text (a slice of at most %d characters), its publication and collection dates, "+
			"the source's other versions, and the fetch attempts that got it. truncated means more text follows: call again with offset.", retrieve.MaxDocumentChars)),
		mcp.WithString("id", mcp.Required(), mcp.Description("The document id (a card's document_id)")),
		mcp.WithNumber("offset", mcp.Description("Character to start at (default 0)")),
		mcp.WithNumber("chars", mcp.Description("Characters to return (default 8000)")),
	)
}

func (t *storeTools) documentHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw, err := req.RequireString("id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	id, err := parseRefID(raw, "D")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	r, err := t.retriever(ctx)
	if err != nil {
		return lookupError(err)
	}
	d, err := r.Document(ctx, id, req.GetInt("offset", 0), req.GetInt("chars", 0))
	if err != nil {
		return lookupError(err)
	}
	return toolResultJSON(d)
}

// --- researchguy_source ---

func sourceTool() mcp.Tool {
	return mcp.NewTool("researchguy_source",
		mcp.WithDescription("A source's history: the documents fetched from it, the searches and page opens that saw it, every fetch attempt "+
			"(failures included, such as a paywall or block), and the runs that cited it. Accepts any URL variant that normalizes to it."),
		mcp.WithString("ref", mcp.Required(), mcp.Description("A URL, a source ref (S:123) or a source id")),
	)
}

func (t *storeTools) sourceHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ref, err := req.RequireString("ref")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	r, err := t.retriever(ctx)
	if err != nil {
		return lookupError(err)
	}
	s, err := r.Source(ctx, ref)
	if err != nil {
		return lookupError(err)
	}
	return toolResultJSON(s)
}

// --- researchguy_ingest_url ---

func ingestURLTool() mcp.Tool {
	return mcp.NewTool("researchguy_ingest_url",
		mcp.WithDescription("Fetch a URL into the store now, index it and embed it, so researchguy_find returns it. A blocked DOI is tried through OpenAlex "+
			"for an abstract and open-access copies. A page that can't be fetched (paywall, bot check, 404) is recorded and reported in errors, never bypassed. "+
			"Without run_id the fetch gets a run of its own (kind manual)."),
		mcp.WithString("url", mcp.Required(), mcp.Description("The http(s) URL to fetch")),
		mcp.WithString("run_id", mcp.Description("Record the fetch in this run instead")),
	)
}

func (t *storeTools) ingestURLHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	u, err := req.RequireString("url")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	r, err := t.retriever(ctx)
	if err != nil {
		return lookupError(err)
	}
	in := &retrieve.Ingester{Index: r.Index, Store: r.Store, Stage: fetch.NewStage(t.cfg.Store.Fetch, r.Store),
		Embed: embed.New(t.cfg), EmbedBudget: t.cfg.Store.Embed.BudgetDuration()}
	res, err := in.IngestURL(ctx, u, strings.TrimSpace(req.GetString("run_id", "")))
	if err != nil {
		return lookupError(err)
	}
	return toolResultJSON(res)
}
