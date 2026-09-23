package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/marklubin/researchguy/internal/config"
)

type stubProvider struct {
	name      string
	resp      string
	responses []string // if set, cycled per call (last one repeats); overrides resp
	err       error
	lastReq   Request
	mu        sync.Mutex
	calls     int
	record    func(prompt string) // optional: called with UserPrompt on each Complete
}

func (s *stubProvider) Name() string { return s.name }

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
	// Call 1 = groundedness critic (no marker -> whole output is the revised text).
	// Call 2 = narrative critic (flags nothing).
	verifier := &stubProvider{name: "claude", responses: []string{
		"verified synthesis",
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
	if !strings.HasPrefix(resp, "verified synthesis") {
		t.Fatalf("response = %q, want to start with %q", resp, "verified synthesis")
	}
	if !strings.Contains(resp, "## Critic Notes") {
		t.Errorf("response should include visible critic notes, got: %s", resp)
	}
	if !strings.Contains(resp, "No narrative-only claims found.") {
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
		"revised answer text" + "\n" + criticChangesMarker + "\n- Removed claim about X: unsupported by any worker.",
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
	if !strings.HasPrefix(resp, "revised answer text") {
		t.Fatalf("response should start with the revised text (marker stripped), got: %s", resp)
	}
	if strings.Contains(strings.SplitN(resp, "## Critic Notes", 2)[0], criticChangesMarker) {
		t.Error("the raw changes marker should not leak into the answer body")
	}
	if !strings.Contains(resp, "### Groundedness Review") {
		t.Errorf("response should include a groundedness review section, got: %s", resp)
	}
	if !strings.Contains(resp, "Removed claim about X") {
		t.Errorf("response should include the reported change, got: %s", resp)
	}
	if !strings.Contains(h.Metadata(), "Removed claim about X") {
		t.Errorf("metadata should record groundedness_changes, got: %s", h.Metadata())
	}
}

func TestSplitCriticOutput(t *testing.T) {
	revised, changes := splitCriticOutput("answer text\n" + criticChangesMarker + "\n- change one")
	if revised != "answer text" {
		t.Errorf("revised = %q, want %q", revised, "answer text")
	}
	if changes != "- change one" {
		t.Errorf("changes = %q, want %q", changes, "- change one")
	}
}

func TestSplitCriticOutput_NoMarker(t *testing.T) {
	revised, changes := splitCriticOutput("just the answer, no marker")
	if revised != "just the answer, no marker" {
		t.Errorf("revised = %q", revised)
	}
	if changes != "" {
		t.Errorf("changes = %q, want empty when marker absent", changes)
	}
}
