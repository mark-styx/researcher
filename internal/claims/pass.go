package claims

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
)

// Options shape an extraction pass.
type Options struct {
	// Limit is the most texts extracted, 0 for no limit.
	Limit int
	// MaxAttempts is how many passes a text with failed chunks gets.
	MaxAttempts int
	// Pause, when set, is checked before each chunk; true stops the pass
	// as if its budget ran out (the daemon yields to research tasks).
	Pause func() bool
	// Embedder, when set, embeds the new claims after they're indexed.
	Embedder index.Embedder
	// Progress, when set, gets a line per text.
	Progress func(format string, args ...any)
}

// Stats is what a pass did.
type Stats struct {
	Extractor string `json:"extractor"`
	Waiting   int    `json:"waiting"`   // texts without a complete extraction before the pass
	Extracted int    `json:"extracted"` // texts the model answered for
	Claims    int    `json:"claims"`    // claims in the texts extracted
	Failed    int    `json:"failed"`    // texts left with failed chunks they'll retry
	Exhausted int    `json:"exhausted"` // texts out of attempts, skipped
	Missing   int    `json:"missing"`   // texts not in the store
	Stopped   bool   `json:"stopped"`   // the budget ran out or the pass paused

	Sync  *index.ClaimStats `json:"sync,omitempty"`
	Embed *index.EmbedStats `json:"embed,omitempty"`
}

// Run extracts claims from the texts of the index's primary documents
// that have none from ex yet, cited ones first, until ctx ends, then
// indexes what it wrote. Each text's extraction is written to the store as
// soon as it's done, so a pass cut short keeps its work.
func Run(ctx context.Context, ix *index.Index, st *store.Store, ex *Extractor, opts Options) (Stats, error) {
	stats := Stats{Extractor: ex.Name()}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 3
	}
	progress := opts.Progress
	if progress == nil {
		progress = func(string, ...any) {}
	}
	cands, err := ix.ClaimCandidates(ctx, store.ExtractorDir(ex.Name()))
	if err != nil {
		return stats, fmt.Errorf("listing documents waiting for claims: %w", err)
	}
	stats.Waiting = len(cands)
	var runErr error
	wrote, used := false, false
	for _, c := range cands {
		if opts.Limit > 0 && stats.Extracted == opts.Limit {
			break
		}
		if ctx.Err() != nil || (opts.Pause != nil && opts.Pause()) {
			stats.Stopped = true
			break
		}
		prev, ok, err := st.ReadExtraction(ex.Name(), c.TextSHA)
		if err != nil {
			progress("%s: reading its extraction: %v; extracting again", short(c.TextSHA), err)
			ok = false
		}
		var prevp *store.Extraction
		if ok {
			if prev.Complete() {
				wrote = true // indexed by the sync below
				continue
			}
			if prev.Attempts >= opts.MaxAttempts {
				stats.Exhausted++
				continue
			}
			prevp = &prev
		}
		text, err := st.ReadText(c.TextSHA)
		if err != nil {
			stats.Missing++
			continue
		}
		used = true
		start := time.Now()
		e, ran, err := ex.Extract(ctx, c.Title, c.TextSHA, text, prevp, opts.Pause)
		if ran {
			if perr := st.PutExtraction(e); perr != nil {
				return stats, fmt.Errorf("writing the extraction of %s: %w", c.TextSHA, perr)
			}
			wrote = true
			stats.Extracted++
			stats.Claims += len(e.Claims)
			if !e.Complete() {
				stats.Failed++
			}
			progress("%s %q: %d claims from %d chunks in %s, %d chunks failed", short(c.TextSHA), c.Title, len(e.Claims), e.Chunks, time.Since(start).Round(time.Second), len(e.Failed))
		}
		if err != nil {
			runErr = err
			break
		}
		if interrupted(e) {
			stats.Stopped = true
			break
		}
	}
	if used {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		if uerr := ex.Unload(uctx); uerr != nil {
			progress("unloading %s: %v", ex.Model, uerr)
		}
		cancel()
	}
	if !wrote {
		return stats, runErr
	}
	// The budget running out shouldn't leave the pass's work unindexed; a
	// cancel (Ctrl-C, the daemon stopping) leaves it for the next pass.
	sctx := ctx
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		var cancel context.CancelFunc
		sctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
	}
	if sctx.Err() != nil {
		return stats, runErr
	}
	cs, err := ix.SyncClaims(sctx, st)
	stats.Sync = &cs
	if err != nil {
		return stats, errors.Join(runErr, fmt.Errorf("indexing claims: %w", err))
	}
	if opts.Embedder != nil {
		es, err := ix.EmbedClaims(sctx, st, opts.Embedder)
		stats.Embed = &es
		if err != nil && sctx.Err() == nil {
			return stats, errors.Join(runErr, fmt.Errorf("embedding claims: %w", err))
		}
	}
	return stats, runErr
}

// interrupted reports whether e has chunks the pass didn't reach.
func interrupted(e store.Extraction) bool {
	for _, f := range e.Failed {
		if f.Error == Interrupted {
			return true
		}
	}
	return false
}

func short(sha string) string { return sha[:min(len(sha), 12)] }
