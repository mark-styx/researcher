package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/critique"
)

type stubProvider struct {
	name      string
	resp      string
	responses []string // if set, cycled per call (last one repeats); overrides resp
	err       error
	evidence  []EvidenceRecord
	metadata  string
	lastReq   Request
	mu        sync.Mutex
	calls     int
	record    func(prompt string) // optional: called with UserPrompt on each Complete
	unload    func() error
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) Metadata() string { return s.metadata }

func (s *stubProvider) Complete(_ context.Context, req Request) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastReq = req
	if s.record != nil {
		s.record(req.UserPrompt)
	}
	if s.err != nil {
		s.calls++
		return "", s.err
	}
	resp := s.resp
	if len(s.responses) > 0 {
		idx := s.calls
		if idx >= len(s.responses) {
			idx = len(s.responses) - 1
		}
		resp = s.responses[idx]
	}
	s.calls++
	return resp, nil
}

func (s *stubProvider) Evidence() []EvidenceRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]EvidenceRecord(nil), s.evidence...)
}

func (s *stubProvider) Unload(context.Context) error {
	if s.unload != nil {
		return s.unload()
	}
	return nil
}

func TestHybridComplete_AggregatesWorkerOutputs(t *testing.T) {
	cfg := &config.Config{
		Claude: config.ClaudeConfig{Model: "opus"},
		Ollama: config.OllamaConfig{Model: "qwen3"},
	}
	aggregator := &stubProvider{name: "claude", resp: "final synthesis"}

	h := &Hybrid{
		cfg:               cfg,
		WorkerBackend:     "ollama",
		WorkerModels:      []string{"m1", "m2"},
		AggregatorBackend: "claude",
		AggregatorModel:   "opus",
		MaxParallel:       2,
		makeProvider: func(backend, model string) (Provider, error) {
			switch backend + "/" + model {
			case "ollama/m1":
				return &stubProvider{name: "m1", resp: "worker draft one"}, nil
			case "ollama/m2":
				return &stubProvider{name: "m2", resp: "worker draft two"}, nil
			case "claude/opus":
				return aggregator, nil
			default:
				return nil, fmt.Errorf("unexpected provider %s/%s", backend, model)
			}
		},
	}

	resp, err := h.Complete(context.Background(), Request{
		SystemPrompt: "base system",
		UserPrompt:   "analyze topic",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "final synthesis" {
		t.Fatalf("response = %q, want %q", resp, "final synthesis")
	}
	if aggregator.calls != 1 {
		t.Fatalf("aggregator calls = %d, want 1", aggregator.calls)
	}
	if !strings.Contains(aggregator.lastReq.UserPrompt, "worker draft one") {
		t.Errorf("aggregator prompt missing worker output one")
	}
	if !strings.Contains(aggregator.lastReq.UserPrompt, "worker draft two") {
		t.Errorf("aggregator prompt missing worker output two")
	}
	if !strings.Contains(aggregator.lastReq.UserPrompt, "analyze topic") {
		t.Errorf("aggregator prompt missing original request")
	}
	if !strings.Contains(h.Metadata(), `"mode":"hybrid"`) {
		t.Errorf("expected hybrid metadata JSON, got %q", h.Metadata())
	}
}

func TestPlanShards_DefaultMode(t *testing.T) {
	got := planShards("topic", "", 4)
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	for _, s := range got {
		found := false
		for _, d := range defaultBranches {
			if s == d {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("shard %q not in defaultBranches", s)
		}
	}
}

func TestPlanShards_UnknownModeFallsBackToDefault(t *testing.T) {
	got := planShards("topic", "bogus-mode", 4)
	want := planShards("topic", "", 4)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("unknown mode should fall back to default branches, got %v", got)
	}
}

func TestPlanShards_LandscapeMode(t *testing.T) {
	got := planShards("topic", "landscape", len(branchSets["landscape"]))
	for i, want := range branchSets["landscape"] {
		if got[i] != want {
			t.Errorf("shard %d = %q, want %q", i, got[i], want)
		}
	}
}

func TestPlanShards_InquiryMode(t *testing.T) {
	got := planShards("topic", "inquiry", len(branchSets["inquiry"]))
	for i, want := range branchSets["inquiry"] {
		if got[i] != want {
			t.Errorf("shard %d = %q, want %q", i, got[i], want)
		}
	}
}

func TestPlanShards_WantExceedsSetSize_Cycles(t *testing.T) {
	got := planShards("topic", "landscape", 6)
	set := branchSets["landscape"]
	if len(got) != 6 {
		t.Fatalf("len = %d, want 6", len(got))
	}
	for i, s := range got {
		if s != set[i%len(set)] {
			t.Errorf("shard %d = %q, want %q (cycled)", i, s, set[i%len(set)])
		}
	}
}

func TestPlanShards_WantZero_ReturnsFullSet(t *testing.T) {
	got := planShards("topic", "inquiry", 0)
	if strings.Join(got, "|") != strings.Join(branchSets["inquiry"], "|") {
		t.Errorf("want=0 should return the full mode set unmodified, got %v", got)
	}
}

func TestHybridComplete_AllWorkersFail(t *testing.T) {
	cfg := &config.Config{
		Claude: config.ClaudeConfig{Model: "opus"},
		Ollama: config.OllamaConfig{Model: "qwen3"},
	}
	h := &Hybrid{
		cfg:               cfg,
		WorkerBackend:     "ollama",
		WorkerModels:      []string{"m1"},
		AggregatorBackend: "claude",
		AggregatorModel:   "opus",
		makeProvider: func(backend, model string) (Provider, error) {
			if backend == "ollama" && model == "m1" {
				return &stubProvider{name: "m1", err: fmt.Errorf("worker failed")}, nil
			}
			return &stubProvider{name: "claude", resp: "unused"}, nil
		},
	}

	_, err := h.Complete(context.Background(), Request{UserPrompt: "test"})
	if err == nil {
		t.Fatal("expected error when all workers fail")
	}
	if !strings.Contains(err.Error(), "all hybrid workers failed") {
		t.Fatalf("error = %q, want all hybrid workers failed", err.Error())
	}
}

func TestHybridComplete_BranchCountExceedsModels(t *testing.T) {
	cfg := &config.Config{
		Claude: config.ClaudeConfig{Model: "opus"},
		Ollama: config.OllamaConfig{Model: "qwen3"},
	}
	aggregator := &stubProvider{name: "claude", resp: "final synthesis"}

	h := &Hybrid{
		cfg:               cfg,
		WorkerBackend:     "ollama",
		WorkerModels:      []string{"m1", "m2"},
		AggregatorBackend: "claude",
		AggregatorModel:   "opus",
		MaxParallel:       4,
		makeProvider: func(backend, model string) (Provider, error) {
			if backend == "claude" && model == "opus" {
				return aggregator, nil
			}
			return &stubProvider{name: model, resp: "draft from " + model}, nil
		},
	}

	_, err := h.Complete(context.Background(), Request{
		UserPrompt:  "analyze topic",
		BranchCount: 4,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 2 worker models, 4 branches requested -> 4 worker calls, models reused round-robin.
	if !strings.Contains(aggregator.lastReq.UserPrompt, "Worker 4") {
		t.Errorf("expected 4 worker outputs in aggregation prompt, got: %s", aggregator.lastReq.UserPrompt)
	}
}

func TestHybridComplete_ModeSelectsBranches(t *testing.T) {
	cfg := &config.Config{
		Claude: config.ClaudeConfig{Model: "opus"},
		Ollama: config.OllamaConfig{Model: "qwen3"},
	}
	var capturedPrompts []string
	var mu sync.Mutex

	h := &Hybrid{
		cfg:               cfg,
		WorkerBackend:     "ollama",
		WorkerModels:      []string{"m1"},
		AggregatorBackend: "claude",
		AggregatorModel:   "opus",
		MaxParallel:       1,
		makeProvider: func(backend, model string) (Provider, error) {
			if backend == "claude" {
				return &stubProvider{name: "claude", resp: "final"}, nil
			}
			return &stubProvider{name: model, resp: "worker draft", record: func(p string) {
				mu.Lock()
				defer mu.Unlock()
				capturedPrompts = append(capturedPrompts, p)
			}}, nil
		},
	}

	_, err := h.Complete(context.Background(), Request{
		UserPrompt:  "is X true",
		Mode:        "inquiry",
		BranchCount: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(capturedPrompts) != 1 {
		t.Fatalf("expected 1 worker prompt, got %d", len(capturedPrompts))
	}
	// BranchCount: 1 selects only the first inquiry-mode shard.
	if !strings.Contains(capturedPrompts[0], "primary evidence directly supporting the claim") {
		t.Errorf("expected inquiry-mode shard in worker prompt, got: %s", capturedPrompts[0])
	}
}

func TestHybridComplete_VerificationPass(t *testing.T) {
	cfg := &config.Config{
		Claude: config.ClaudeConfig{Model: "opus"},
		Ollama: config.OllamaConfig{Model: "qwen3"},
		Hybrid: config.HybridConfig{
			EnableVerification: true,
			VerifierBackend:    "claude",
			VerifierModel:      "sonnet",
		},
	}
	aggregator := &stubProvider{name: "claude", resp: "draft synthesis"}
	// Call 1 = flag-only groundedness critic. Call 2 = narrative critic.
	verifier := &stubProvider{name: "claude", responses: []string{
		"- Claim X is not supported by the evidence ledger.",
		"No narrative-only claims found.",
	}}

	h := &Hybrid{
		cfg:               cfg,
		WorkerBackend:     "ollama",
		WorkerModels:      []string{"m1"},
		AggregatorBackend: "claude",
		AggregatorModel:   "opus",
		MaxParallel:       1,
		makeProvider: func(backend, model string) (Provider, error) {
			switch backend + "/" + model {
			case "ollama/m1":
				return &stubProvider{name: "m1", resp: "worker draft"}, nil
			case "claude/opus":
				return aggregator, nil
			case "claude/sonnet":
				return verifier, nil
			default:
				return nil, fmt.Errorf("unexpected provider %s/%s", backend, model)
			}
		},
	}

	resp, err := h.Complete(context.Background(), Request{UserPrompt: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(resp, "draft synthesis") {
		t.Fatalf("verification rewrote the draft: %q", resp)
	}
	if !strings.Contains(resp, "## Critic Notes") {
		t.Errorf("response should include visible critic notes, got: %s", resp)
	}
	if !strings.Contains(resp, "Claim X is not supported") || !strings.Contains(resp, "No narrative-only claims found.") {
		t.Errorf("response should include the narrative critic's finding, got: %s", resp)
	}
	if verifier.calls != 2 {
		t.Fatalf("verifier calls = %d, want 2 (groundedness + narrative)", verifier.calls)
	}
	if !strings.Contains(h.Metadata(), `"verified":true`) {
		t.Fatalf("metadata should record verified=true, got %q", h.Metadata())
	}
}

func TestHybridComplete_GroundednessCriticReportsChanges(t *testing.T) {
	cfg := &config.Config{
		Claude: config.ClaudeConfig{Model: "opus"},
		Ollama: config.OllamaConfig{Model: "qwen3"},
		Hybrid: config.HybridConfig{
			EnableVerification: true,
			VerifierBackend:    "claude",
			VerifierModel:      "sonnet",
		},
	}
	aggregator := &stubProvider{name: "claude", resp: "draft synthesis"}
	verifier := &stubProvider{name: "claude", responses: []string{
		"- Claim about X: unsupported by the evidence ledger.",
		"No narrative-only claims found.",
	}}

	h := &Hybrid{
		cfg:               cfg,
		WorkerBackend:     "ollama",
		WorkerModels:      []string{"m1"},
		AggregatorBackend: "claude",
		AggregatorModel:   "opus",
		MaxParallel:       1,
		makeProvider: func(backend, model string) (Provider, error) {
			switch backend + "/" + model {
			case "ollama/m1":
				return &stubProvider{name: "m1", resp: "worker draft"}, nil
			case "claude/opus":
				return aggregator, nil
			case "claude/sonnet":
				return verifier, nil
			default:
				return nil, fmt.Errorf("unexpected provider %s/%s", backend, model)
			}
		},
	}

	resp, err := h.Complete(context.Background(), Request{UserPrompt: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(resp, "draft synthesis") {
		t.Fatalf("groundedness critic rewrote the draft: %s", resp)
	}
	if !strings.Contains(resp, "### Groundedness Review") {
		t.Errorf("response should include a groundedness review section, got: %s", resp)
	}
	if !strings.Contains(resp, "Claim about X") {
		t.Errorf("response should include the reported flag, got: %s", resp)
	}
	if !strings.Contains(h.Metadata(), "Claim about X") || !strings.Contains(h.Metadata(), `"groundedness_flags"`) {
		t.Errorf("metadata should record groundedness_flags, got: %s", h.Metadata())
	}
}

func TestHybridComplete_UsesRawToolEvidenceLedger(t *testing.T) {
	cfg := &config.Config{
		Claude: config.ClaudeConfig{Model: "opus"},
		Ollama: config.OllamaConfig{Model: "worker"},
		Hybrid: config.HybridConfig{EnableVerification: true, VerifierBackend: "claude", VerifierModel: "sonnet"},
	}
	aggregator := &stubProvider{name: "claude", resp: "draft synthesis", metadata: `{"model":"aggregator"}`}
	verifier := &stubProvider{name: "claude", responses: []string{
		critique.NoUnsupportedClaims,
		critique.NoNarrativeClaims,
	}}
	worker := &stubProvider{
		name:     "worker",
		resp:     "worker interpretation that is not itself evidence",
		metadata: `{"model":"worker","eval_count":20}`,
		evidence: []EvidenceRecord{{
			Label:   "web_fetch url=https://example.com/source",
			Content: "Primary source text from the fetched page.",
		}},
	}
	h := &Hybrid{
		cfg:                cfg,
		WorkerBackend:      "ollama",
		WorkerModels:       []string{"worker"},
		AggregatorBackend:  "claude",
		AggregatorModel:    "opus",
		VerifierBackend:    "claude",
		VerifierModel:      "sonnet",
		EnableVerification: true,
		MaxParallel:        1,
		makeProvider: func(backend, model string) (Provider, error) {
			switch backend + "/" + model {
			case "ollama/worker":
				return worker, nil
			case "claude/opus":
				return aggregator, nil
			case "claude/sonnet":
				return verifier, nil
			default:
				return nil, fmt.Errorf("unexpected provider %s/%s", backend, model)
			}
		},
	}

	if _, err := h.Complete(context.Background(), Request{UserPrompt: "test"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(aggregator.lastReq.UserPrompt, "Evidence ledger") || !strings.Contains(aggregator.lastReq.UserPrompt, "Primary source text") {
		t.Errorf("aggregator did not receive raw evidence ledger: %s", aggregator.lastReq.UserPrompt)
	}
	if !strings.Contains(verifier.lastReq.UserPrompt, "Primary source text") {
		t.Errorf("verifier did not receive raw evidence ledger: %s", verifier.lastReq.UserPrompt)
	}
	if strings.Contains(verifier.lastReq.UserPrompt, "worker interpretation that is not itself evidence") {
		t.Errorf("worker prose was treated as verification evidence: %s", verifier.lastReq.UserPrompt)
	}
	metadata := h.Metadata()
	if !strings.Contains(metadata, `"llm_metadata":{"model":"worker","eval_count":20}`) || !strings.Contains(metadata, `"aggregator_metadata":{"model":"aggregator"}`) {
		t.Errorf("hybrid metadata dropped Ollama telemetry: %s", metadata)
	}
}

func TestHybridComplete_UnloadsEachStageBeforeNextModel(t *testing.T) {
	cfg := &config.Config{
		Claude: config.ClaudeConfig{Model: "opus"},
		Ollama: config.OllamaConfig{Model: "worker"},
		Hybrid: config.HybridConfig{EnableVerification: true, VerifierBackend: "ollama", VerifierModel: "verifier"},
	}
	var eventsMu sync.Mutex
	var events []string
	record := func(event string) {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		events = append(events, event)
	}
	provider := func(model string, responses []string) *stubProvider {
		return &stubProvider{
			name:      model,
			responses: responses,
			record:    func(string) { record(model + ":complete") },
			unload:    func() error { record(model + ":unload"); return nil },
		}
	}
	worker := provider("worker", []string{"worker draft"})
	aggregator := provider("aggregator", []string{"draft"})
	verifier := provider("verifier", []string{critique.NoUnsupportedClaims, critique.NoNarrativeClaims})
	h := &Hybrid{
		cfg: cfg, WorkerBackend: "ollama", WorkerModels: []string{"worker"},
		AggregatorBackend: "ollama", AggregatorModel: "aggregator",
		VerifierBackend: "ollama", VerifierModel: "verifier", EnableVerification: true, MaxParallel: 1,
		makeProvider: func(_ string, model string) (Provider, error) {
			switch model {
			case "worker":
				return worker, nil
			case "aggregator":
				return aggregator, nil
			case "verifier":
				return verifier, nil
			default:
				return nil, fmt.Errorf("unexpected model %s", model)
			}
		},
	}
	if _, err := h.Complete(context.Background(), Request{UserPrompt: "test"}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(events, ",")
	want := "worker:complete,worker:unload,aggregator:complete,aggregator:unload,verifier:complete,verifier:complete,verifier:unload"
	if got != want {
		t.Fatalf("lifecycle = %q, want %q", got, want)
	}
}
