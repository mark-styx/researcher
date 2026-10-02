package claims

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
)

// LinkOptions shape a linking pass (store.claims.link).
type LinkOptions struct {
	Neighbors      int
	MinSimilarity  float64
	BatchSize      int
	MaxPairsPerDay int
	// Model is recorded on each link: the backend and model, claude/sonnet.
	Model    string
	Pause    func() bool
	Progress func(format string, args ...any)
}

// LinkStats is what a linking pass did.
type LinkStats struct {
	Model      string         `json:"model"`
	Checked    int            `json:"checked"` // claims compared with their nearest claims
	Pairs      int            `json:"pairs"`   // pairs sent to the model
	Linked     int            `json:"linked"`  // links logged
	ByRelation map[string]int `json:"by_relation,omitempty"`
	// Unanswered is pairs the model's answer left out or got wrong; they
	// aren't asked again until the claims are re-indexed.
	Unanswered int  `json:"unanswered"`
	Capped     bool `json:"capped"` // stopped at max_pairs_per_day
	Stopped    bool `json:"stopped"`

	Sync *index.ClaimStats `json:"sync,omitempty"`
}

// NewLinker returns the model and options store.claims.link configures.
// Links record the model as backend/model, claude/sonnet.
func NewLinker(cfg *config.Config) (llm.Provider, LinkOptions, error) {
	l := cfg.Store.Claims.Link
	model := l.Backend
	if l.Model != "" {
		model += "/" + l.Model
	}
	opts := LinkOptions{Neighbors: l.Neighbors, MinSimilarity: l.MinSimilarity, BatchSize: l.BatchSize,
		MaxPairsPerDay: l.MaxPairsPerDay, Model: model}
	p, err := llm.NewProvider(cfg, l.Backend, l.Model)
	if err != nil {
		return nil, opts, fmt.Errorf("store.claims.link: %w", err)
	}
	return p, opts, nil
}

// claimPage is how many unchecked claims one round of a pass takes.
const claimPage = 100

// Link compares claims the linker hasn't checked with their nearest
// claims from other origins and has p label each pair, logging the labels
// to the store's links.jsonl, at most MaxPairsPerDay pairs a day. A pair
// needs a cosine similarity of MinSimilarity and a word in common to be
// asked about. A claim counts as checked once its pairs have an answer.
func Link(ctx context.Context, ix *index.Index, st *store.Store, p llm.Provider, opts LinkOptions) (LinkStats, error) {
	stats := LinkStats{Model: opts.Model, ByRelation: map[string]int{}}
	if opts.Neighbors <= 0 {
		opts.Neighbors = 5
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 20
	}
	progress := opts.Progress
	if progress == nil {
		progress = func(string, ...any) {}
	}
	used, err := ix.ModelLinksSince(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		return stats, err
	}
	left := opts.MaxPairsPerDay - used
	if opts.MaxPairsPerDay <= 0 {
		left = int(^uint(0) >> 1)
	}
	if left <= 0 {
		stats.Capped = true
		return stats, nil
	}

	seen := map[[2]int64]bool{}
	var batch []pair
	var owners []int64 // claims whose pairs are all in batch or answered
	var runErr error
	logged := false
	flush := func() error {
		if len(batch) > 0 {
			links, unanswered, err := label(ctx, p, batch, opts.Model)
			if err != nil {
				return err
			}
			if err := st.AppendLinks(links); err != nil {
				return fmt.Errorf("logging links: %w", err)
			}
			logged = logged || len(links) > 0
			left -= len(batch)
			stats.Pairs += len(batch)
			stats.Linked += len(links)
			stats.Unanswered += unanswered
			for _, l := range links {
				stats.ByRelation[l.Relation]++
			}
			progress("labeled %d pair(s): %d linked, %d unanswered", len(batch), len(links), unanswered)
		}
		if err := ix.MarkLinkChecked(context.WithoutCancel(ctx), owners); err != nil {
			return err
		}
		stats.Checked += len(owners)
		batch, owners = nil, nil
		return nil
	}

pass:
	for {
		claims, err := ix.UncheckedClaims(ctx, claimPage)
		if err != nil {
			runErr = err
			break
		}
		if len(claims) == 0 {
			break
		}
		for _, a := range claims {
			if ctx.Err() != nil || (opts.Pause != nil && opts.Pause()) {
				stats.Stopped = true
				break pass
			}
			near, err := ix.NearestClaims(ctx, a.ID, opts.Neighbors, opts.MinSimilarity)
			if err != nil {
				runErr = err
				break pass
			}
			var mine []pair
			for _, b := range near {
				key := [2]int64{min(a.ID, b.ID), max(a.ID, b.ID)}
				if seen[key] || !shareTerm(a.Text, b.Text) {
					continue
				}
				mine = append(mine, pair{a, b})
			}
			if len(mine) > left-len(batch) {
				stats.Capped = true
				break pass
			}
			for _, pr := range mine {
				seen[[2]int64{min(pr.a.ID, pr.b.ID), max(pr.a.ID, pr.b.ID)}] = true
			}
			batch = append(batch, mine...)
			owners = append(owners, a.ID)
			if len(batch) >= opts.BatchSize {
				if err := flush(); err != nil {
					runErr = err
					break pass
				}
			}
		}
		// The page's claims are marked before the next page is read, or
		// it would read them again.
		if err := flush(); err != nil {
			runErr = err
			break
		}
	}
	// Pairs already gathered are labeled even when the pass stops, unless
	// the model failed or ctx ended.
	if runErr == nil && ctx.Err() == nil {
		runErr = flush()
	}
	if logged {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		cs, err := ix.SyncClaims(sctx, st)
		stats.Sync = &cs
		if err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("indexing links: %w", err))
		}
	}
	return stats, runErr
}

type pair struct{ a, b index.LinkClaim }

const linkSystemPrompt = `You label how pairs of claims from different sources relate. For each pair of claims A and B, choose one relation:
- same: they assert the same thing
- supports: one is evidence for the other without asserting the same thing
- contradicts: they can't both be true as of the same date
- refines: one is a more specific, qualified or corrected version of the other
- supersedes: one is a later statement of the same changing fact (a newer count, a changed status) that replaces the other
- unrelated: none of these
For refines and supersedes, set "by" to the claim that refines or supersedes the other, "A" or "B". Use the dates given: a claim with a later date that gives a new value for the same fact supersedes the older one rather than contradicting it. Judge only from the claims as written; don't use what you know about the topic. Give a confidence from 0 to 1, and for anything but unrelated a one-sentence note on why.
Answer with JSON only, no prose: {"labels":[{"pair":1,"relation":"same","by":"","confidence":0.9,"note":"..."}]}`

// linkPrompt lays the pairs out for the model.
func linkPrompt(batch []pair) string {
	var b strings.Builder
	describe := func(name string, c index.LinkClaim) {
		fmt.Fprintf(&b, "%s: %s\n", name, c.Text)
		fmt.Fprintf(&b, "   quote: %q\n", c.Quote)
		var src []string
		if c.Title != "" {
			src = append(src, fmt.Sprintf("source %q", c.Title))
		}
		if c.Published != "" {
			src = append(src, "published "+c.Published)
		}
		if c.AsOf != "" {
			src = append(src, "true as of "+c.AsOf)
		}
		if len(src) > 0 {
			fmt.Fprintf(&b, "   %s\n", strings.Join(src, ", "))
		}
	}
	for i, pr := range batch {
		fmt.Fprintf(&b, "Pair %d\n", i+1)
		describe("A", pr.a)
		describe("B", pr.b)
		b.WriteString("\n")
	}
	return b.String()
}

// label asks p about batch and returns the links its answer gives, and
// how many pairs it left out or answered wrongly. An error is the call
// failing; an answer that isn't JSON leaves every pair unanswered.
func label(ctx context.Context, p llm.Provider, batch []pair, model string) ([]store.Link, int, error) {
	out, err := p.Complete(ctx, llm.Request{SystemPrompt: linkSystemPrompt, UserPrompt: linkPrompt(batch), MaxTokens: 4096})
	if err != nil {
		return nil, 0, fmt.Errorf("labeling claim pairs with %s: %w", model, err)
	}
	var answer struct {
		Labels []struct {
			Pair       int     `json:"pair"`
			Relation   string  `json:"relation"`
			By         string  `json:"by"`
			Confidence float64 `json:"confidence"`
			Note       string  `json:"note"`
		} `json:"labels"`
	}
	start, end := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if start < 0 || end < start || json.Unmarshal([]byte(out[start:end+1]), &answer) != nil {
		return nil, len(batch), nil
	}
	now := time.Now().UTC()
	done := map[int]bool{}
	var links []store.Link
	for _, l := range answer.Labels {
		i := l.Pair - 1
		if i < 0 || i >= len(batch) || done[i] {
			continue
		}
		pr := batch[i]
		from, to := min(pr.a.ID, pr.b.ID), max(pr.a.ID, pr.b.ID)
		rel := strings.ToLower(strings.TrimSpace(l.Relation))
		if rel == store.RelRefines || rel == store.RelSupersedes {
			switch strings.ToUpper(strings.TrimSpace(l.By)) {
			case "A":
				from, to = pr.a.ID, pr.b.ID
			case "B":
				from, to = pr.b.ID, pr.a.ID
			default:
				continue
			}
		}
		note := strings.TrimSpace(l.Note)
		if len(note) > 300 {
			note = note[:300]
		}
		link := store.Link{From: from, To: to, Relation: rel, Method: store.LinkModel, Model: model,
			Confidence: max(0, min(1, l.Confidence)), Note: note, CreatedAt: now}
		if link.Check() != nil {
			continue
		}
		done[i] = true
		links = append(links, link)
	}
	return links, len(batch) - len(links), nil
}

// shareTerm reports whether a and b have a word of four or more letters in
// common, or a number, besides the commonest words: two claims that share
// no word are close in meaning only by accident of the embedding.
func shareTerm(a, b string) bool {
	words := func(s string) map[string]bool {
		out := map[string]bool{}
		for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			digit := strings.IndexFunc(w, unicode.IsDigit) >= 0
			if (len(w) >= 4 || digit) && !stopWords[w] {
				out[w] = true
			}
		}
		return out
	}
	wa := words(a)
	for w := range words(b) {
		if wa[w] {
			return true
		}
	}
	return false
}

var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`that this with from have been were which their there they than also into more most such some other
		about after before over under when what where while these those would could should will each only many much very
		between through during because does said says them then upon within without across among being both even
		like made make same than very well`) {
		stopWords[w] = true
	}
}
