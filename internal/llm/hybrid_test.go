package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/marklubin/researcher/internal/config"
)

type stubProvider struct {
	name    string
	resp    string
	err     error
	lastReq Request
	mu      sync.Mutex
	calls   int
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) Complete(_ context.Context, req Request) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastReq = req
	s.calls++
	if s.err != nil {
		return "", s.err
	}
	return s.resp, nil
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
	verifier := &stubProvider{name: "claude", resp: "verified synthesis"}

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
	if resp != "verified synthesis" {
		t.Fatalf("response = %q, want %q", resp, "verified synthesis")
	}
	if verifier.calls != 1 {
		t.Fatalf("verifier calls = %d, want 1", verifier.calls)
	}
	if !strings.Contains(h.Metadata(), `"verified":true`) {
		t.Fatalf("metadata should record verified=true, got %q", h.Metadata())
	}
}
