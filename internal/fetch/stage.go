package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/graph"
	"github.com/marklubin/researchguy/internal/store"
)

// DefaultUsableChars is how much text counts as having the document: the
// 2026-10-02 yield measurement's threshold. Shorter text from a DOI source
// sends the fetcher to OpenAlex.
const DefaultUsableChars = 3000

// maxRescues caps the open-access copies tried for one DOI.
const maxRescues = 4

// Stage is a run's fetch stage.
type Stage struct {
	Store       *store.Store
	Client      *Client
	OpenAlex    *OpenAlex // nil skips the DOI path
	PDF         PDFTools
	TopResults  int
	Concurrency int
	Budget      time.Duration // 0 is no limit
	UsableChars int
	Now         func() time.Time
}

// NewStage builds the fetch stage store.fetch configures.
func NewStage(cfg config.StoreFetchConfig, st *store.Store) *Stage {
	client := NewClient(cfg.TimeoutDuration())
	if cfg.UserAgent != "" {
		client.UserAgent = cfg.UserAgent
	}
	s := &Stage{
		Store:       st,
		Client:      client,
		PDF:         FindPDFTools(),
		TopResults:  cfg.TopResults,
		Concurrency: cfg.Concurrency,
		Budget:      cfg.BudgetDuration(),
		UsableChars: DefaultUsableChars,
	}
	if cfg.OpenAlex {
		s.OpenAlex = &OpenAlex{Client: client}
	}
	return s
}

func (s *Stage) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Run fetches what Plan lists for the run, skipping URLs an earlier pass
// already tried unless force is set. It writes every attempt to the run's
// fetches.jsonl as it ends and fetch.json at the end. URLs the budget
// doesn't reach count as Remaining and are fetched by the next pass. It
// returns store.ErrFetchBusy when another process is fetching for the run.
func (s *Stage) Run(ctx context.Context, runID string, force bool) (store.FetchSummary, error) {
	dir := s.Store.RunDir(runID)
	rec, err := store.ReadRecord(dir)
	if err != nil {
		return store.FetchSummary{}, err
	}
	log, err := store.OpenFetchLog(dir)
	if err != nil {
		return store.FetchSummary{}, err
	}
	defer log.Close()
	captures, err := store.ScanCaptures(dir)
	if err != nil {
		return store.FetchSummary{}, err
	}
	prior, err := store.ScanFetches(dir)
	if err != nil {
		return store.FetchSummary{}, err
	}
	tried := map[string]bool{}
	for _, r := range prior.Records {
		if k, err := graph.NormalizeURL(r.URL); err == nil {
			tried[k] = true
		}
	}

	sum := store.FetchSummary{StartedAt: s.now()}
	var todo []Target
	for _, t := range Plan(dir, rec, captures.Captures, s.TopResults) {
		sum.Queued++
		if tried[t.Key] && !force {
			sum.Skipped++
			continue
		}
		todo = append(todo, t)
	}

	if s.Budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.Budget)
		defer cancel()
	}
	ctx, abort := context.WithCancelCause(ctx)
	defer abort(nil)

	var mu sync.Mutex
	emit := func(r store.FetchRecord) {
		if _, err := log.Append(r); err != nil {
			abort(fmt.Errorf("writing fetch log: %w", err))
			return
		}
		mu.Lock()
		sum.Records++
		mu.Unlock()
	}
	jobs := make(chan Target)
	var wg sync.WaitGroup
	for range max(s.Concurrency, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				got, reached := s.fetchTarget(ctx, t, emit)
				mu.Lock()
				switch {
				case !reached:
					sum.Remaining++
				case got:
					sum.Fetched++
				default:
					sum.Failed++
				}
				mu.Unlock()
			}
		}()
	}
dispatch:
	for i, t := range todo {
		select {
		case jobs <- t:
		case <-ctx.Done():
			mu.Lock()
			sum.Remaining += len(todo) - i
			mu.Unlock()
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
		return sum, cause
	}
	sum.FinishedAt = s.now()
	if err := log.WriteSummary(sum); err != nil {
		return sum, err
	}
	return sum, nil
}

// fetchTarget fetches one target: the URL itself, then for a DOI whose
// page gave too little text, OpenAlex's abstract and the open-access copies
// it lists. It reports whether any attempt got text, and reached is false
// when the stage's context ended before the first attempt finished.
func (s *Stage) fetchTarget(ctx context.Context, t Target, emit func(store.FetchRecord)) (got, reached bool) {
	rec, ok := s.attempt(ctx, t, t.URL, ViaDirect)
	if !ok {
		return false, false
	}
	emit(rec)
	usable := s.usable()
	got = rec.TextSHA256 != ""
	if rec.TextChars >= usable || s.OpenAlex == nil {
		return got, true
	}
	doi := firstNonEmpty(t.DOI, rec.DOI)
	if doi == "" {
		return got, true
	}
	work, ok := s.openAlex(ctx, t, doi, emit)
	if !ok || work == nil {
		return got, true
	}
	if work.Abstract != "" {
		got = true
	}
	seen := map[string]bool{t.Key: true}
	if k, err := graph.NormalizeURL(rec.FinalURL); err == nil {
		seen[k] = true
	}
	tries := 0
	for _, c := range work.Candidates() {
		k, err := graph.NormalizeURL(c.URL)
		if err != nil || seen[k] {
			continue
		}
		seen[k] = true
		if tries++; tries > maxRescues {
			break
		}
		r, ok := s.attempt(ctx, t, c.URL, c.Via)
		if !ok {
			break
		}
		emit(r)
		if r.TextSHA256 != "" {
			got = true
		}
		if r.TextChars >= usable {
			break
		}
	}
	return got, true
}

// openAlex records OpenAlex's answer for doi: the work's title and date,
// and its abstract as a document when it has one.
func (s *Stage) openAlex(ctx context.Context, t Target, doi string, emit func(store.FetchRecord)) (*Work, bool) {
	start := time.Now()
	rec := store.FetchRecord{URL: t.URL, FetchURL: s.OpenAlex.WorkURL(doi), Reason: t.Reason, Rank: t.Rank, Via: ViaOpenAlex, AttemptedAt: s.now(), DOI: doi}
	work, resp, err := s.OpenAlex.Work(ctx, doi)
	if ctx.Err() != nil {
		return nil, false
	}
	rec.DurationMS = time.Since(start).Milliseconds()
	rec.Attempts = 1
	if resp != nil {
		rec.HTTPStatus, rec.Attempts, rec.ContentType = resp.Status, resp.Attempts, resp.ContentType
	}
	var ae *AttemptError
	if errors.As(err, &ae) {
		rec.Attempts = ae.Attempts
	}
	switch {
	case err != nil:
		rec.Error = err.Error()
	case work == nil:
		rec.Error = "not in OpenAlex"
	default:
		rec.Title = work.Title
		rec.Published = work.Published(s.now())
		if abstract := cleanText(work.Abstract); abstract != "" {
			sha, err := s.Store.PutText(abstract)
			if err != nil {
				rec.Error = "storing text: " + err.Error()
			} else {
				rec.TextSHA256, rec.TextChars, rec.ContentKind = sha, utf8.RuneCountInString(abstract), store.KindAbstract
			}
		}
	}
	emit(rec)
	return work, true
}

// maxRefreshHops caps meta-refresh redirects followed in one attempt.
const maxRefreshHops = 2

// attempt fetches fetchURL for t and extracts its text. ok is false when
// the stage's context ended first, in which case nothing is recorded: the
// URL is still to do, not failed.
func (s *Stage) attempt(ctx context.Context, t Target, fetchURL, via string) (store.FetchRecord, bool) {
	start := time.Now()
	rec := store.FetchRecord{URL: t.URL, Reason: t.Reason, Rank: t.Rank, Via: via, AttemptedAt: s.now(), DOI: t.DOI}
	if fetchURL != t.URL {
		rec.FetchURL = fetchURL
	}
	resp, err := s.Client.Get(ctx, fetchURL)
	attempts := 0
	for hop := 0; err == nil; hop++ {
		attempts += resp.Attempts
		if hop >= maxRefreshHops || resp.Status != http.StatusOK || kindOf(resp) != "html" {
			break
		}
		page := ParseHTML(resp.Body, resp.ContentType, resp.FinalURL, s.now())
		if page.Refresh == "" || page.Refresh == resp.FinalURL || utf8.RuneCountInString(page.Text) >= s.usable() {
			break
		}
		next, nerr := s.Client.Get(ctx, page.Refresh)
		if nerr != nil {
			break
		}
		resp = next
	}
	if ctx.Err() != nil {
		return rec, false
	}
	rec.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		rec.Error = err.Error()
		rec.Attempts = 1
		var ae *AttemptError
		if errors.As(err, &ae) {
			rec.Attempts = ae.Attempts
		}
		return rec, true
	}
	rec.Attempts = attempts
	rec.HTTPStatus = resp.Status
	rec.ContentType = resp.ContentType
	if resp.FinalURL != fetchURL {
		rec.FinalURL = resp.FinalURL
	}
	if resp.Status < 200 || resp.Status > 299 {
		rec.Error = fmt.Sprintf("HTTP %d", resp.Status)
		return rec, true
	}
	kind := kindOf(resp)
	if kind == "" {
		rec.Error = "not a document: " + firstNonEmpty(resp.ContentType, "no content type")
		return rec, true
	}
	sha, err := s.Store.PutBlob(resp.Body)
	if err != nil {
		rec.Error = "storing raw bytes: " + err.Error()
		return rec, true
	}
	rec.RawSHA256, rec.RawBytes = sha, int64(len(resp.Body))

	now := s.now()
	var text string
	switch kind {
	case "html":
		page := ParseHTML(resp.Body, resp.ContentType, resp.FinalURL, now)
		text, rec.Title, rec.Published = page.Text, page.Title, page.Published
		if page.DOI != "" && rec.DOI == "" {
			rec.DOI = page.DOI
		}
	case "pdf":
		if resp.Truncated {
			rec.Error = fmt.Sprintf("PDF larger than %d bytes, not extracted", s.Client.MaxBytes)
			return rec, true
		}
		pdf, err := s.PDF.Extract(ctx, resp.Body, now)
		if err != nil {
			rec.Error = err.Error()
			return rec, true
		}
		text, rec.Title, rec.Published = pdf.Text, pdf.Title, urlPathDate(resp.FinalURL, now)
		if rec.Published == nil {
			rec.Published = pdf.Created
		}
	case "text":
		text = strings.TrimSpace(strings.ToValidUTF8(string(resp.Body), "�"))
	}
	if rec.Published == nil {
		rec.Published = urlPathDate(resp.FinalURL, now)
	}
	if rec.Published == nil && resp.LastModified != "" {
		rec.Published = published(resp.LastModified, "last-modified", true, now)
	}
	if text = cleanText(text); text == "" {
		rec.Error = "no text extracted"
		return rec, true
	}
	tsha, err := s.Store.PutText(text)
	if err != nil {
		rec.Error = "storing text: " + err.Error()
		return rec, true
	}
	rec.TextSHA256, rec.TextChars, rec.ContentKind = tsha, utf8.RuneCountInString(text), store.KindFull
	return rec, true
}

// cleanText makes text storable in Postgres, which refuses NUL bytes and
// invalid UTF-8, and trims it.
func cleanText(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	return strings.TrimSpace(strings.ReplaceAll(s, "\x00", ""))
}

func (s *Stage) usable() int {
	if s.UsableChars > 0 {
		return s.UsableChars
	}
	return DefaultUsableChars
}

// kindOf classifies a response body: html, pdf, text, or "" for anything
// that isn't a document (images, archives, JSON).
func kindOf(resp *Response) string {
	if bytes.HasPrefix(resp.Body, []byte("%PDF-")) {
		return "pdf"
	}
	mt, _, _ := mime.ParseMediaType(resp.ContentType)
	switch {
	case mt == "text/html" || mt == "application/xhtml+xml":
		return "html"
	case mt == "application/pdf":
		return "pdf"
	case mt == "text/plain" || mt == "text/markdown":
		return "text"
	case mt == "":
		head := bytes.ToLower(resp.Body[:min(len(resp.Body), 512)])
		if bytes.Contains(head, []byte("<html")) || bytes.Contains(head, []byte("<!doctype html")) {
			return "html"
		}
	}
	return ""
}
