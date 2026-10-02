package retrieve

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marklubin/researchguy/internal/fetch"
	"github.com/marklubin/researchguy/internal/graph"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
)

// Ingester fetches URLs into the store and the index on request.
type Ingester struct {
	Index *index.Index
	Store *store.Store
	Stage *fetch.Stage
	// Embed embeds what was fetched before IngestURL returns, within
	// EmbedBudget (0 is no limit); nil leaves it for the daemon or
	// `researchguy store embed`.
	Embed       index.Embedder
	EmbedBudget time.Duration
}

// IngestedDocument is a document an ingest fetched.
type IngestedDocument struct {
	DocumentID  int64   `json:"document_id"`
	SourceID    int64   `json:"source_id"`
	SourceRef   string  `json:"source_ref"`
	URL         string  `json:"url"`
	Via         string  `json:"via"`
	ContentKind string  `json:"content_kind"`
	TextChars   int     `json:"text_chars"`
	Title       string  `json:"title,omitempty"`
	Published   *Date   `json:"published,omitempty"`
	Passages    []int64 `json:"passages"`
}

// IngestResult is what IngestURL did.
type IngestResult struct {
	RunID     string             `json:"run_id"`
	URL       string             `json:"url"`
	Fetch     store.FetchSummary `json:"fetch"`
	Documents []IngestedDocument `json:"documents"`
	// Errors are the failed attempts' errors, in order.
	Errors   []string `json:"errors,omitempty"`
	Embedded int      `json:"embedded"`
	// Note says what was left undone, such as passages left unembedded.
	Note string `json:"note,omitempty"`
}

// KindManual is the run kind of an ingest asked for without a run.
const KindManual = "manual"

// IngestURL fetches rawURL now (store.ReasonIngest) for runID, or for a
// new manual run when runID is blank, then indexes the run and embeds the
// documents it got. A URL that can't be fetched is not an error: the
// attempt is recorded and the result says why.
func (in *Ingester) IngestURL(ctx context.Context, rawURL, runID string) (IngestResult, error) {
	rawURL = strings.TrimSpace(rawURL)
	if !fetch.Fetchable(rawURL) {
		return IngestResult{}, fmt.Errorf("%q is not an http(s) URL of a document", rawURL)
	}
	res := IngestResult{URL: rawURL, Documents: []IngestedDocument{}}
	var run *store.Run
	var err error
	if runID == "" {
		if run, err = in.Store.StartRun(store.RunRecord{Kind: KindManual, Topic: rawURL, Backend: "fetch"}); err != nil {
			return res, err
		}
		runID = run.ID()
	} else if _, err := store.ReadRecord(in.Store.RunDir(runID)); err != nil {
		return res, fmt.Errorf("run %s: %w", runID, err)
	}
	res.RunID = runID

	before, err := store.ScanFetches(in.Store.RunDir(runID))
	if err != nil {
		return res, err
	}
	last := 0
	for _, r := range before.Records {
		last = max(last, r.Seq)
	}
	res.Fetch, err = in.Stage.FetchURLs(ctx, runID, []string{rawURL})
	if run != nil {
		fin := store.Finish{Status: store.StatusSucceeded}
		if err != nil {
			fin = store.Finish{Status: store.StatusFailed, Error: err.Error()}
		} else if res.Fetch.Fetched == 0 {
			fin = store.Finish{Status: store.StatusFailed, Error: "nothing fetched"}
		}
		if ferr := run.Finish(fin); ferr != nil && err == nil {
			err = ferr
		}
	}
	if err != nil {
		return res, err
	}

	after, err := store.ScanFetches(in.Store.RunDir(runID))
	if err != nil {
		return res, err
	}
	if _, err := in.Index.IngestRun(ctx, in.Store.RunDir(runID), false); err != nil {
		return res, fmt.Errorf("indexing run %s: %w", runID, err)
	}
	seen := map[int64]bool{}
	var docIDs []int64
	for _, r := range after.Records {
		if r.Seq <= last {
			continue
		}
		if r.TextSHA256 == "" {
			if r.Error != "" {
				res.Errors = append(res.Errors, r.Via+": "+r.Error)
			}
			continue
		}
		rkey, err := graph.NormalizeURL(r.URL)
		if err != nil {
			continue
		}
		id := index.DocumentID(rkey, r.TextSHA256)
		if seen[id] {
			continue
		}
		seen[id] = true
		docIDs = append(docIDs, id)
		d := IngestedDocument{DocumentID: id, SourceID: index.SourceID(rkey), SourceRef: SourceRef(index.SourceID(rkey)), URL: r.URL,
			Via: r.Via, ContentKind: r.ContentKind, TextChars: r.TextChars, Title: r.Title}
		if r.Published != nil {
			d.Published = &Date{Date: r.Published.Date, Precision: r.Published.Precision, From: r.Published.From, Weak: r.Published.Weak}
		}
		res.Documents = append(res.Documents, d)
	}
	if len(docIDs) > 0 && in.Embed != nil {
		ectx, cancel := ctx, context.CancelFunc(func() {})
		if in.EmbedBudget > 0 {
			ectx, cancel = context.WithTimeout(ctx, in.EmbedBudget)
		}
		stats, err := in.Index.EmbedDocuments(ectx, in.Store, in.Embed, docIDs)
		cancel()
		res.Embedded = stats.Embedded + stats.FromCache
		if err != nil {
			res.Note = fmt.Sprintf("passages not embedded yet (%v); the daemon or `researchguy store embed` retries", err)
		}
	} else if len(docIDs) > 0 {
		res.Note = "passages not embedded: no embedding model configured"
	}
	for i := range res.Documents {
		rows, err := in.Index.Pool().Query(ctx, `SELECT id FROM passages WHERE document_id = $1 ORDER BY ord`, res.Documents[i].DocumentID)
		if err != nil {
			return res, err
		}
		if res.Documents[i].Passages, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
			return res, err
		}
		if res.Documents[i].Passages == nil {
			res.Documents[i].Passages = []int64{}
		}
	}
	if len(res.Documents) == 0 && len(res.Errors) == 0 && res.Fetch.Remaining > 0 {
		res.Errors = append(res.Errors, "the fetch budget ran out before the URL was tried")
	}
	return res, nil
}
