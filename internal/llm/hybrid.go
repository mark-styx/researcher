package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/critique"
	"github.com/marklubin/researchguy/internal/store"
)

// defaultHybridEvidenceChars caps the evidence ledger one prompt receives
// when no cap is set and the consumer isn't claude.
const defaultHybridEvidenceChars = 80_000

// claudeEvidenceChars is the default ledger budget for a claude aggregator
// or critic. At 3-4 chars per token it is ~500-670k tokens, which leaves
// room in Opus's 1M-token window for the worker drafts, research context
// and the report. The 2026-10-02 inquiry dives captured 0.83-1.01M chars,
// so a run like those passes whole.
const claudeEvidenceChars = 2_000_000

// The aggregator writes its report between these lines. Anything outside
// them (status chatter, offers to save files) is kept in the run record
// and left out of the report.
const (
	reportBegin = "===BEGIN REPORT==="
	reportEnd   = "===END REPORT==="
)

type hybridWorkerOutput struct {
	Index    int
	Backend  string
	Model    string
	Shard    string
	Content  string
	Evidence []critique.Evidence
	Provider Provider
	Metadata json.RawMessage
	Err      error
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
	// MaxEvidenceChars caps how much of the raw evidence ledger one prompt
	// receives, split evenly across shards. <= 0 falls back to
	// hybrid.max_evidence_chars, then a default for the consumer's backend
	// (claudeEvidenceChars or defaultHybridEvidenceChars). The full ledger
	// goes to the run record regardless.
	MaxEvidenceChars int

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
	started := time.Now()

	makeProvider := h.makeProvider
	if makeProvider == nil {
		makeProvider = func(backend, model string) (Provider, error) {
			return NewProvider(h.cfg, backend, model)
		}
	}

	workerBackend := normalizeWorkerBackend(h.WorkerBackend)
	models := h.resolveWorkerModels(workerBackend)
	if len(models) == 0 {
		return "", fmt.Errorf("hybrid backend has no worker models configured")
	}

	parallel := h.MaxParallel
	if parallel <= 0 {
		parallel = 1
	}
	branchCount := req.BranchCount
	if branchCount <= 0 {
		if roles, ok := branchSets[req.Mode]; ok {
			branchCount = len(roles)
		} else {
			branchCount = len(models)
		}
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
				Index:    idx,
				Backend:  workerBackend,
				Model:    m,
				Shard:    sh,
				Content:  resp,
				Evidence: providerEvidence(p, workerBackend, m, sh),
				Provider: p,
				Metadata: providerMetadataJSON(p),
				Err:      err,
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
	// Number every captured item and write it, with the worker drafts, to
	// the run record before anything else can fail. The numbers are the
	// [E<seq>] IDs the aggregator and critics cite.
	assignEvidenceIDs(results)
	st := hybridState{req: req, shards: shards, workers: results, started: started}
	st.storeErr(recordWorkers(req.Run, results)...)

	if err := unloadWorkers(ctx, results); err != nil {
		return "", fmt.Errorf("unloading hybrid workers before aggregation: %w", err)
	}
	if success == 0 {
		h.setMetadata(st.metadataJSON())
		return "", fmt.Errorf("all hybrid workers failed: %s", strings.Join(failures, "; "))
	}

	st.aggBackend = normalizeAggregatorBackend(h.AggregatorBackend)
	st.aggModel = h.AggregatorModel
	if strings.TrimSpace(st.aggModel) == "" {
		st.aggModel = h.cfg.Claude.Model
	}
	aggCap := h.evidenceCapFor(st.aggBackend)
	evidence, ledger := buildLedger(results, aggCap)
	st.ledger = ledger

	aggregator, err := makeProvider(st.aggBackend, st.aggModel)
	if err != nil {
		h.setMetadata(st.metadataJSON())
		return "", fmt.Errorf("creating hybrid aggregator provider: %w", err)
	}

	aggReq := Request{
		SystemPrompt: aggregationSystemPrompt(req.SystemPrompt),
		UserPrompt:   buildAggregationPrompt(req, results, evidence),
		MaxTokens:    req.MaxTokens,
	}
	st.storeErr(req.Run.WriteFile("aggregator-prompt.md", promptRecord(aggReq)))
	raw, err := aggregator.Complete(ctx, aggReq)
	if err != nil {
		_ = unloadProvider(ctx, aggregator)
		h.setMetadata(st.metadataJSON())
		return "", err
	}
	st.storeErr(req.Run.WriteFile("aggregator-output.md", []byte(raw)))
	draft, output := extractReport(raw)
	st.output = &output
	if output.Markers != markersOK {
		fmt.Fprintf(os.Stderr, "Warning: hybrid aggregator report markers %s; kept %d chars as the report\n", output.Markers, output.ReportChars)
	}
	st.aggMetadata = providerMetadataJSON(aggregator)
	if err := unloadProvider(ctx, aggregator); err != nil {
		h.setMetadata(st.metadataJSON())
		return "", fmt.Errorf("unloading hybrid aggregator before verification: %w", err)
	}

	final := draft
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
			verifierModel = st.aggModel
		}
		// The critics judge the draft against the same ledger it was
		// written from, unless their backend has a smaller budget.
		criticEvidence := evidence
		if criticCap := h.evidenceCapFor(verifierBackend); criticCap != aggCap {
			var criticLedger ledgerStats
			criticEvidence, criticLedger = buildLedger(results, criticCap)
			st.criticLedger = &criticLedger
		}
		verifier, verr := makeProvider(verifierBackend, verifierModel)
		if verr == nil {
			// Groundedness is flag-only. It validates the unchanged draft
			// against raw tool results rather than treating worker prose as a
			// source of truth.
			groundReq := Request{
				SystemPrompt: critique.GroundednessFlagSystemPrompt(),
				UserPrompt:   critique.BuildGroundednessFlagPrompt(draft, criticEvidence),
				MaxTokens:    req.MaxTokens,
			}
			if raw, rerr := verifier.Complete(ctx, groundReq); rerr == nil && strings.TrimSpace(raw) != "" {
				st.groundednessFlags = strings.TrimSpace(raw)
				st.verified = true
			} else if rerr != nil {
				st.groundednessFlags = fmt.Sprintf("Groundedness verification failed: %v", rerr)
			}
			st.groundednessVerifierMetadata = providerMetadataJSON(verifier)

			// Narrative-detector critic: does not rewrite the draft. It flags
			// claims presented as settled/consensus that aren't tied to
			// distinct evidence in the worker outputs — separating "here's
			// what the evidence shows" from "here's what's widely repeated."
			narrReq := Request{
				SystemPrompt: critique.NarrativeSystemPrompt(),
				UserPrompt:   critique.BuildNarrativePrompt(final, criticEvidence),
				MaxTokens:    req.MaxTokens,
			}
			if flags, nerr := verifier.Complete(ctx, narrReq); nerr == nil {
				st.narrativeFlags = strings.TrimSpace(flags)
			} else {
				st.narrativeFlags = fmt.Sprintf("Narrative verification failed: %v", nerr)
			}
			st.narrativeVerifierMetadata = providerMetadataJSON(verifier)
			if uerr := unloadProvider(ctx, verifier); uerr != nil {
				st.narrativeFlags = strings.TrimSpace(st.narrativeFlags + "\nVerifier unload failed: " + uerr.Error())
			}
		} else {
			st.groundednessFlags = fmt.Sprintf("Verification provider failed: %v", verr)
		}
	}

	final = critique.AppendNotes(final, st.groundednessFlags, st.narrativeFlags)

	h.setMetadata(st.metadataJSON())
	return final, nil
}

func (h *Hybrid) resolveWorkerModels(workerBackend string) []string {
	candidates := h.WorkerModels
	if len(candidates) == 0 && h.cfg != nil {
		// Fall back to the worker backend's own model, never another
		// backend's: an Ollama model name means nothing to Codex.
		fallback := h.cfg.Ollama.Model
		if workerBackend == "codex" {
			fallback = h.cfg.Codex.Model
		}
		if strings.TrimSpace(fallback) != "" {
			candidates = []string{fallback}
		}
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
	suffix := "You are the final aggregator. Synthesize the worker drafts, resolve conflicts, avoid duplication, and produce one coherent answer.\n\n" +
		"Write the final report, and nothing else, between a line containing only " + reportBegin + " and a line containing only " + reportEnd + ". " +
		"Everything outside those lines is discarded, so put no notes, status updates, offers or questions there. Do not describe saving or writing files: the report text is the whole deliverable."
	if strings.TrimSpace(base) == "" {
		return suffix
	}
	return strings.TrimSpace(base) + "\n\n" + suffix
}

func buildAggregationPrompt(original Request, workers []hybridWorkerOutput, evidence []critique.Evidence) string {
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
	b.WriteString("\nEvidence ledger (raw successful tool results; cite an item by its ID, e.g. [E12], for factual claims):\n")
	if len(evidence) == 0 {
		b.WriteString("\n(no raw tool evidence captured)\n")
	} else {
		for _, e := range evidence {
			fmt.Fprintf(&b, "\n[%s]\n%s\n", e.Heading(), e.Content)
		}
	}
	b.WriteString("\nProduce the best final answer using the worker analysis, but ground factual claims in the evidence ledger. Mark claims without ledger support as analysis or uncertainty. ")
	b.WriteString("Write the report between a " + reportBegin + " line and a " + reportEnd + " line.")
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

// workerEvidence returns only raw successful tool results. Worker prose is
// useful analysis for aggregation, but is not promoted into source evidence.
func workerEvidence(workers []hybridWorkerOutput, maxChars int) []critique.Evidence {
	evidence, _ := buildLedger(workers, maxChars)
	return evidence
}

// ledgerStats records how much raw evidence the workers captured and how much
// of it fit under the cap, so a run's metadata shows what the aggregator saw.
type ledgerStats struct {
	MaxChars       int `json:"max_chars"`
	ItemsCaptured  int `json:"items_captured"`
	ItemsPassed    int `json:"items_passed"`
	CharsCaptured  int `json:"chars_captured"`
	CharsPassed    int `json:"chars_passed"`
	ShardsCaptured int `json:"shards_captured"`
	ShardsPassed   int `json:"shards_passed"`
}

// buildLedger caps the successful workers' evidence at maxChars in total,
// split evenly across workers. Filling the cap in shard order let the first
// shard (primary evidence, in inquiry mode) take the whole ledger and drop
// the counter-evidence shards. A worker that needs less than its share
// passes the remainder to the others. Each worker keeps its items in order.
func buildLedger(workers []hybridWorkerOutput, maxChars int) ([]critique.Evidence, ledgerStats) {
	if maxChars <= 0 {
		maxChars = defaultHybridEvidenceChars
	}
	stats := ledgerStats{MaxChars: maxChars}
	var groups [][]critique.Evidence
	var needs []int
	for _, w := range workers {
		if w.Err != nil || len(w.Evidence) == 0 {
			continue
		}
		need := evidenceChars(w.Evidence)
		groups = append(groups, w.Evidence)
		needs = append(needs, need)
		stats.ItemsCaptured += len(w.Evidence)
		stats.CharsCaptured += need
		stats.ShardsCaptured++
	}

	var out []critique.Evidence
	for i, budget := range fairShares(needs, maxChars) {
		if budget <= 0 {
			continue
		}
		passed := critique.LimitEvidence(groups[i], budget)
		if len(passed) == 0 {
			continue
		}
		out = append(out, passed...)
		stats.ItemsPassed += len(passed)
		stats.CharsPassed += evidenceChars(passed)
		stats.ShardsPassed++
	}
	return out, stats
}

func evidenceChars(evidence []critique.Evidence) int {
	n := 0
	for _, e := range evidence {
		n += len(e.Content)
	}
	return n
}

// fairShares splits total across needs by water-filling. Going from the
// smallest need up, each gets the lesser of its need and an even split of
// what is left, so unused share flows to the needs that are still open.
// The allocations never sum past total.
func fairShares(needs []int, total int) []int {
	alloc := make([]int, len(needs))
	order := make([]int, len(needs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return needs[order[a]] < needs[order[b]] })
	remaining := total
	for k, i := range order {
		give := min(needs[i], remaining/(len(order)-k))
		alloc[i] = give
		remaining -= give
	}
	return alloc
}

// evidenceCapFor is the ledger budget for one prompt sent to backend: an
// explicit cap if one is set, otherwise sized to the backend's context.
func (h *Hybrid) evidenceCapFor(backend string) int {
	if h.MaxEvidenceChars > 0 {
		return h.MaxEvidenceChars
	}
	if h.cfg != nil && h.cfg.Hybrid.MaxEvidenceChars > 0 {
		return h.cfg.Hybrid.MaxEvidenceChars
	}
	if backend == "claude" {
		return claudeEvidenceChars
	}
	return defaultHybridEvidenceChars
}

func providerEvidence(provider Provider, backend, model, shard string) []critique.Evidence {
	ep, ok := provider.(EvidenceProvider)
	if !ok {
		return nil
	}
	records := ep.Evidence()
	out := make([]critique.Evidence, 0, len(records))
	for _, record := range records {
		if strings.TrimSpace(record.Content) == "" {
			continue
		}
		out = append(out, critique.Evidence{
			Label:   fmt.Sprintf("%s/%s | shard: %s | %s", backend, model, shard, record.Label),
			Content: record.Content,
		})
	}
	return out
}

func unloadWorkers(ctx context.Context, workers []hybridWorkerOutput) error {
	var failures []string
	for _, worker := range workers {
		if worker.Provider == nil {
			continue
		}
		if err := unloadProvider(ctx, worker.Provider); err != nil {
			failures = append(failures, fmt.Sprintf("%s/%s: %v", worker.Backend, worker.Model, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func unloadProvider(ctx context.Context, provider Provider) error {
	unloader, ok := provider.(Unloader)
	if !ok {
		return nil
	}
	return unloader.Unload(ctx)
}

func providerMetadataJSON(provider Provider) json.RawMessage {
	mp, ok := provider.(MetadataProvider)
	if !ok {
		return nil
	}
	metadata := strings.TrimSpace(mp.Metadata())
	if metadata == "" {
		return nil
	}
	if json.Valid([]byte(metadata)) {
		return json.RawMessage(metadata)
	}
	encoded, _ := json.Marshal(metadata)
	return json.RawMessage(encoded)
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

// hybridState accumulates what one Complete call did, for its metadata.
type hybridState struct {
	req                          Request
	shards                       []string
	workers                      []hybridWorkerOutput
	started                      time.Time
	aggBackend                   string
	aggModel                     string
	aggMetadata                  json.RawMessage
	ledger                       ledgerStats
	criticLedger                 *ledgerStats
	output                       *aggregatorOutput
	verified                     bool
	groundednessFlags            string
	narrativeFlags               string
	groundednessVerifierMetadata json.RawMessage
	narrativeVerifierMetadata    json.RawMessage
	storeErrors                  []string
}

// storeErr records failed run-record writes. They don't stop the run, but
// they're printed and kept in the metadata, because they mean evidence
// that should be on disk isn't.
func (r *hybridState) storeErr(errs ...error) {
	for _, err := range errs {
		if err == nil {
			continue
		}
		fmt.Fprintf(os.Stderr, "Warning: writing run record: %v\n", err)
		r.storeErrors = append(r.storeErrors, err.Error())
	}
}

func (r *hybridState) metadataJSON() string {
	type workerMeta struct {
		Backend       string          `json:"backend"`
		Model         string          `json:"model"`
		Shard         string          `json:"shard"`
		Error         string          `json:"error,omitempty"`
		Output        string          `json:"output"`
		EvidenceItems int             `json:"evidence_items"`
		LLMMetadata   json.RawMessage `json:"llm_metadata,omitempty"`
	}
	meta := struct {
		Mode                         string            `json:"mode"`
		BranchMode                   string            `json:"branch_mode,omitempty"`
		OriginalPrompt               string            `json:"original_prompt"`
		Shards                       []string          `json:"shards"`
		Workers                      []workerMeta      `json:"workers"`
		AggregatorBackend            string            `json:"aggregator_backend"`
		AggregatorModel              string            `json:"aggregator_model"`
		AggregatorMetadata           json.RawMessage   `json:"aggregator_metadata,omitempty"`
		AggregatorOutput             *aggregatorOutput `json:"aggregator_output,omitempty"`
		EvidenceLedger               ledgerStats       `json:"evidence_ledger"`
		CriticEvidenceLedger         *ledgerStats      `json:"critic_evidence_ledger,omitempty"`
		Verified                     bool              `json:"verified"`
		GroundednessFlags            string            `json:"groundedness_flags,omitempty"`
		NarrativeFlags               string            `json:"narrative_flags,omitempty"`
		GroundednessVerifierMetadata json.RawMessage   `json:"groundedness_verifier_metadata,omitempty"`
		NarrativeVerifierMetadata    json.RawMessage   `json:"narrative_verifier_metadata,omitempty"`
		StoreErrors                  []string          `json:"store_errors,omitempty"`
		DurationMS                   int64             `json:"duration_ms"`
	}{
		Mode:                         "hybrid",
		BranchMode:                   r.req.Mode,
		OriginalPrompt:               truncateForMetadata(r.req.UserPrompt, 2000),
		Shards:                       r.shards,
		AggregatorBackend:            r.aggBackend,
		AggregatorModel:              r.aggModel,
		AggregatorMetadata:           r.aggMetadata,
		AggregatorOutput:             r.output,
		EvidenceLedger:               r.ledger,
		CriticEvidenceLedger:         r.criticLedger,
		Verified:                     r.verified,
		GroundednessFlags:            truncateForMetadata(r.groundednessFlags, 2000),
		NarrativeFlags:               truncateForMetadata(r.narrativeFlags, 2000),
		GroundednessVerifierMetadata: r.groundednessVerifierMetadata,
		NarrativeVerifierMetadata:    r.narrativeVerifierMetadata,
		StoreErrors:                  r.storeErrors,
		DurationMS:                   time.Since(r.started).Milliseconds(),
	}
	for _, w := range r.workers {
		m := workerMeta{
			Backend:       w.Backend,
			Model:         w.Model,
			Shard:         w.Shard,
			Output:        truncateForMetadata(w.Content, 4000),
			EvidenceItems: len(w.Evidence),
			LLMMetadata:   w.Metadata,
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

// assignEvidenceIDs numbers every captured item E1..En in worker order,
// failed workers included, so the IDs match captures.jsonl.
func assignEvidenceIDs(workers []hybridWorkerOutput) {
	seq := 0
	for wi := range workers {
		for ei := range workers[wi].Evidence {
			seq++
			workers[wi].Evidence[ei].ID = fmt.Sprintf("E%d", seq)
		}
	}
}

// recordWorkers writes every worker's captures and full draft to the run.
// Evidence must already carry its IDs.
func recordWorkers(run *store.Run, workers []hybridWorkerOutput) []error {
	if run == nil {
		return nil
	}
	var captures []store.Capture
	var drafts bytes.Buffer
	enc := json.NewEncoder(&drafts)
	for _, w := range workers {
		var werr string
		if w.Err != nil {
			werr = w.Err.Error()
		}
		for _, e := range w.Evidence {
			seq, _ := strconv.Atoi(strings.TrimPrefix(e.ID, "E"))
			captures = append(captures, store.Capture{
				Seq:         seq,
				Worker:      w.Index + 1,
				Shard:       w.Shard,
				Backend:     w.Backend,
				Model:       w.Model,
				WorkerError: werr,
				Label:       e.Label,
				Content:     e.Content,
			})
		}
		_ = enc.Encode(struct {
			Worker        int             `json:"worker"`
			Backend       string          `json:"backend"`
			Model         string          `json:"model"`
			Shard         string          `json:"shard"`
			Error         string          `json:"error,omitempty"`
			EvidenceItems int             `json:"evidence_items"`
			Content       string          `json:"content"`
			Metadata      json.RawMessage `json:"metadata,omitempty"`
		}{w.Index + 1, w.Backend, w.Model, w.Shard, werr, len(w.Evidence), w.Content, w.Metadata})
	}
	return []error{run.Capture(captures...), run.WriteFile("workers.jsonl", drafts.Bytes())}
}

// promptRecord is how an aggregator prompt is saved in the run record.
func promptRecord(req Request) []byte {
	return []byte("# System prompt\n\n" + req.SystemPrompt + "\n\n# User prompt\n\n" + req.UserPrompt + "\n")
}

const (
	markersOK           = "ok"
	markersUnterminated = "unterminated"
	markersMissing      = "missing"
	markersEmpty        = "empty"
)

// aggregatorOutput says how the report was cut out of the aggregator's raw
// output, which the run record keeps whole.
type aggregatorOutput struct {
	Markers        string `json:"markers"`
	ReportChars    int    `json:"report_chars"`
	DiscardedChars int    `json:"discarded_chars"`
}

// extractReport returns the text between the first begin marker and the
// last end marker after it. With no end marker it keeps everything after
// the begin marker; with no begin marker, or nothing between the markers,
// it keeps the whole output, so a model that ignores the contract still
// yields a report.
func extractReport(raw string) (string, aggregatorOutput) {
	lines := strings.Split(raw, "\n")
	begin, end := -1, -1
	for i, l := range lines {
		if isMarker(l, reportBegin) {
			begin = i
			break
		}
	}
	if begin >= 0 {
		for i := len(lines) - 1; i > begin; i-- {
			if isMarker(lines[i], reportEnd) {
				end = i
				break
			}
		}
	}
	whole := strings.TrimSpace(raw)
	if begin < 0 {
		return whole, aggregatorOutput{Markers: markersMissing, ReportChars: len(whole)}
	}
	before := strings.TrimSpace(strings.Join(lines[:begin], "\n"))
	var report, after string
	status := markersOK
	if end < 0 {
		status = markersUnterminated
		report = strings.TrimSpace(strings.Join(lines[begin+1:], "\n"))
	} else {
		report = strings.TrimSpace(strings.Join(lines[begin+1:end], "\n"))
		after = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	}
	if report == "" {
		return whole, aggregatorOutput{Markers: markersEmpty, ReportChars: len(whole)}
	}
	return report, aggregatorOutput{Markers: status, ReportChars: len(report), DiscardedChars: len(before) + len(after)}
}

// isMarker matches a marker line, tolerating markdown decoration a model
// may add around it (bold, code, heading).
func isMarker(line, marker string) bool {
	return strings.Trim(line, " \t\r*`#>_") == marker
}

func truncateForMetadata(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
