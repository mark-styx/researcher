package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/critique"
)

type hybridWorkerOutput struct {
	Index   int
	Backend string
	Model   string
	Shard   string
	Content string
	Err     error
}

// Hybrid runs multiple worker model calls and then aggregates with a final model.
type Hybrid struct {
	cfg                *config.Config
	WorkerBackend      string
	WorkerModels       []string
	AggregatorBackend  string
	AggregatorModel    string
	VerifierBackend    string
	VerifierModel      string
	EnableVerification bool
	MaxParallel        int

	makeProvider func(backend, model string) (Provider, error)
	mu           sync.RWMutex
	lastMetadata string
}

func (h *Hybrid) Name() string {
	return "hybrid"
}

func (h *Hybrid) Metadata() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lastMetadata
}

func (h *Hybrid) Complete(ctx context.Context, req Request) (string, error) {
	h.setMetadata("")

	makeProvider := h.makeProvider
	if makeProvider == nil {
		makeProvider = func(backend, model string) (Provider, error) {
			return NewProvider(h.cfg, backend, model)
		}
	}

	workerBackend := normalizeWorkerBackend(h.WorkerBackend)
	models := h.resolveWorkerModels()
	if len(models) == 0 {
		return "", fmt.Errorf("hybrid backend has no worker models configured")
	}

	parallel := h.MaxParallel
	if parallel <= 0 {
		parallel = 1
	}
	branchCount := req.BranchCount
	if branchCount <= 0 {
		branchCount = len(models)
	}
	shards := planShards(req.UserPrompt, req.Mode, branchCount)

	// One worker call per shard (the effort/breadth dial), cycling through
	// the configured worker models round-robin. This decouples how many
	// angles get investigated from how many distinct models are configured.
	out := make(chan hybridWorkerOutput, len(shards))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, shard := range shards {
		model := models[i%len(models)]
		workerReq := req
		workerReq.UserPrompt = buildShardPrompt(req.UserPrompt, shard)
		wg.Add(1)
		go func(idx int, m string, sh string, wr Request) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			p, err := makeProvider(workerBackend, m)
			if err != nil {
				out <- hybridWorkerOutput{
					Index: idx, Backend: workerBackend, Model: m, Shard: sh, Err: err,
				}
				return
			}
			resp, err := p.Complete(ctx, wr)
			out <- hybridWorkerOutput{
				Index:   idx,
				Backend: workerBackend,
				Model:   m,
				Shard:   sh,
				Content: resp,
				Err:     err,
			}
		}(i, model, shard, workerReq)
	}
	wg.Wait()
	close(out)

	results := make([]hybridWorkerOutput, len(shards))
	var success int
	var failures []string
	for r := range out {
		results[r.Index] = r
		if r.Err != nil {
			failures = append(failures, fmt.Sprintf("%s/%s: %v", r.Backend, r.Model, r.Err))
			continue
		}
		success++
	}
	if success == 0 {
		return "", fmt.Errorf("all hybrid workers failed: %s", strings.Join(failures, "; "))
	}

	aggregatorBackend := normalizeAggregatorBackend(h.AggregatorBackend)
	aggregatorModel := h.AggregatorModel
	if strings.TrimSpace(aggregatorModel) == "" {
		aggregatorModel = h.cfg.Claude.Model
	}

	aggregator, err := makeProvider(aggregatorBackend, aggregatorModel)
	if err != nil {
		return "", fmt.Errorf("creating hybrid aggregator provider: %w", err)
	}

	started := time.Now()
	aggReq := Request{
		SystemPrompt: aggregationSystemPrompt(req.SystemPrompt),
		UserPrompt:   buildAggregationPrompt(req, results),
		MaxTokens:    req.MaxTokens,
	}
	draft, err := aggregator.Complete(ctx, aggReq)
	if err != nil {
		return "", err
	}

	final := draft
	verified := false
	var groundednessChanges string
	var narrativeFlags string
	if h.shouldVerify() {
		verifierBackend := h.VerifierBackend
		if strings.TrimSpace(verifierBackend) == "" && h.cfg != nil {
			verifierBackend = h.cfg.Hybrid.VerifierBackend
		}
		verifierBackend = normalizeVerifierBackend(verifierBackend)
		verifierModel := strings.TrimSpace(h.VerifierModel)
		if verifierModel == "" && h.cfg != nil {
			verifierModel = strings.TrimSpace(h.cfg.Hybrid.VerifierModel)
		}
		if verifierModel == "" {
			verifierModel = aggregatorModel
		}
		verifier, verr := makeProvider(verifierBackend, verifierModel)
		if verr == nil {
			// Groundedness critic: revises the draft to only keep claims the
			// worker evidence supports. Unlike a plain overwrite, it must
			// report what it changed and why — that report becomes part of
			// the metadata and the visible critic notes, not a silent edit.
			evidence := workerEvidence(results)
			groundReq := Request{
				SystemPrompt: critique.GroundednessRewriteSystemPrompt(),
				UserPrompt:   critique.BuildGroundednessRewritePrompt(req.UserPrompt, draft, evidence),
				MaxTokens:    req.MaxTokens,
			}
			if raw, rerr := verifier.Complete(ctx, groundReq); rerr == nil && strings.TrimSpace(raw) != "" {
				revised, changes := critique.SplitRewriteOutput(raw)
				if strings.TrimSpace(revised) != "" {
					final = revised
					verified = true
					groundednessChanges = changes
				}
			}

			// Narrative-detector critic: does not rewrite the draft. It flags
			// claims presented as settled/consensus that aren't tied to
			// distinct evidence in the worker outputs — separating "here's
			// what the evidence shows" from "here's what's widely repeated."
			narrReq := Request{
				SystemPrompt: critique.NarrativeSystemPrompt(),
				UserPrompt:   critique.BuildNarrativePrompt(final, evidence),
				MaxTokens:    req.MaxTokens,
			}
			if flags, nerr := verifier.Complete(ctx, narrReq); nerr == nil {
				narrativeFlags = strings.TrimSpace(flags)
			}
		}
	}

	final = critique.AppendNotes(final, groundednessChanges, narrativeFlags)

	h.setMetadata(buildHybridMetadataJSON(req, shards, results, aggregatorBackend, aggregatorModel, verified, groundednessChanges, narrativeFlags, time.Since(started)))
	return final, nil
}

func (h *Hybrid) resolveWorkerModels() []string {
	candidates := h.WorkerModels
	if len(candidates) == 0 && h.cfg != nil && strings.TrimSpace(h.cfg.Ollama.Model) != "" {
		candidates = []string{h.cfg.Ollama.Model}
	}

	seen := make(map[string]struct{})
	var out []string
	for _, m := range candidates {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	return out
}

func normalizeWorkerBackend(backend string) string {
	backend = strings.TrimSpace(backend)
	if backend == "" || backend == "hybrid" {
		return "ollama"
	}
	return backend
}

func normalizeAggregatorBackend(backend string) string {
	backend = strings.TrimSpace(backend)
	if backend == "" || backend == "hybrid" {
		return "claude"
	}
	return backend
}

func normalizeVerifierBackend(backend string) string {
	backend = strings.TrimSpace(backend)
	if backend == "" || backend == "hybrid" {
		return "claude"
	}
	return backend
}

func aggregationSystemPrompt(base string) string {
	suffix := "You are the final aggregator. Synthesize the worker drafts, resolve conflicts, avoid duplication, and produce one coherent answer."
	if strings.TrimSpace(base) == "" {
		return suffix
	}
	return strings.TrimSpace(base) + "\n\n" + suffix
}

func buildAggregationPrompt(original Request, workers []hybridWorkerOutput) string {
	var b strings.Builder
	b.WriteString("Original request:\n")
	b.WriteString(original.UserPrompt)
	b.WriteString("\n\nWorker outputs:\n")
	for _, w := range workers {
		if w.Err != nil || strings.TrimSpace(w.Content) == "" {
			continue
		}
		b.WriteString("\n--- Worker ")
		b.WriteString(fmt.Sprintf("%d", w.Index+1))
		b.WriteString(" (")
		b.WriteString(w.Backend)
		b.WriteString("/")
		b.WriteString(w.Model)
		b.WriteString(") ---\n")
		b.WriteString(w.Content)
		b.WriteString("\n")
	}
	b.WriteString("\nProduce the best final answer using the strongest evidence from the worker outputs.")
	return b.String()
}

// defaultBranches is the general-purpose angle set used when mode is "" or unrecognized.
var defaultBranches = []string{
	"core concepts and definitions",
	"recent developments and concrete examples",
	"tradeoffs, risks, and limitations",
	"implementation guidance and practical recommendations",
}

// branchSets holds the epistemic branch-role sets selectable via Request.Mode.
// "landscape" is for tool/alternatives-comparison questions: dry, and treats
// everything except vendor marketing claims as trustworthy by default.
// "inquiry" is for open-ended or contested claims: deeper fanout with
// mandatory counter-evidence and funding-provenance branches.
var branchSets = map[string][]string{
	"landscape": {
		"documented alternatives and how people solve this without the tool/approach in question",
		"vendor and marketing claims, kept separate from independently reported usage and outcomes",
		"competitive positioning: strengths, weaknesses, and gaps relative to alternatives",
		"adoption evidence: who uses this, at what scale, and what they report",
	},
	"inquiry": {
		"primary evidence directly supporting the claim",
		"counter-evidence and disconfirming or null-result cases",
		"funding and institutional provenance of the evidence base",
		"independent replication or corroboration outside the original source",
		"narrative-vs-evidence gap: what's widely repeated versus what's actually substantiated",
	},
}

// IsValidMode reports whether mode is "" or one of the named branch-role sets.
func IsValidMode(mode string) bool {
	if mode == "" {
		return true
	}
	_, ok := branchSets[mode]
	return ok
}

// planShards picks the branch-role set for mode (falling back to
// defaultBranches) and expands or cycles it to exactly want entries.
func planShards(topic string, mode string, want int) []string {
	base := defaultBranches
	if set, ok := branchSets[mode]; ok {
		base = set
	}
	if want <= 0 {
		return base
	}
	var out []string
	for i := 0; i < want; i++ {
		out = append(out, base[i%len(base)])
	}
	return out
}

func buildShardPrompt(original, shard string) string {
	return fmt.Sprintf(`Task: %s

Focus only on this shard: %s.

Return:
1. Key findings (bullets)
2. Evidence or examples
3. Open questions/uncertainty
4. Confidence 0-1
`, original, shard)
}

// workerEvidence labels each successful worker's output for the critics.
func workerEvidence(workers []hybridWorkerOutput) []critique.Evidence {
	var out []critique.Evidence
	for _, w := range workers {
		if w.Err != nil {
			continue
		}
		out = append(out, critique.Evidence{
			Label:   fmt.Sprintf("%s/%s | shard: %s", w.Backend, w.Model, w.Shard),
			Content: w.Content,
		})
	}
	return out
}

func (h *Hybrid) shouldVerify() bool {
	if h.cfg == nil {
		return h.EnableVerification
	}
	return h.EnableVerification || h.cfg.Hybrid.EnableVerification
}

func (h *Hybrid) setMetadata(s string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastMetadata = s
}

func buildHybridMetadataJSON(req Request, shards []string, workers []hybridWorkerOutput, aggBackend, aggModel string, verified bool, groundednessChanges, narrativeFlags string, duration time.Duration) string {
	type workerMeta struct {
		Backend string `json:"backend"`
		Model   string `json:"model"`
		Shard   string `json:"shard"`
		Error   string `json:"error,omitempty"`
		Output  string `json:"output"`
	}
	meta := struct {
		Mode                string       `json:"mode"`
		BranchMode          string       `json:"branch_mode,omitempty"`
		OriginalPrompt      string       `json:"original_prompt"`
		Shards              []string     `json:"shards"`
		Workers             []workerMeta `json:"workers"`
		AggregatorBackend   string       `json:"aggregator_backend"`
		AggregatorModel     string       `json:"aggregator_model"`
		Verified            bool         `json:"verified"`
		GroundednessChanges string       `json:"groundedness_changes,omitempty"`
		NarrativeFlags      string       `json:"narrative_flags,omitempty"`
		DurationMS          int64        `json:"duration_ms"`
	}{
		Mode:                "hybrid",
		BranchMode:          req.Mode,
		OriginalPrompt:      truncateForMetadata(req.UserPrompt, 2000),
		Shards:              shards,
		AggregatorBackend:   aggBackend,
		AggregatorModel:     aggModel,
		Verified:            verified,
		GroundednessChanges: truncateForMetadata(groundednessChanges, 2000),
		NarrativeFlags:      truncateForMetadata(narrativeFlags, 2000),
		DurationMS:          duration.Milliseconds(),
	}
	for _, w := range workers {
		m := workerMeta{
			Backend: w.Backend,
			Model:   w.Model,
			Shard:   w.Shard,
			Output:  truncateForMetadata(w.Content, 4000),
		}
		if w.Err != nil {
			m.Error = w.Err.Error()
		}
		meta.Workers = append(meta.Workers, m)
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(b)
}

func truncateForMetadata(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
