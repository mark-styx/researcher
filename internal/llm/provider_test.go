package llm

import (
	"testing"

	"github.com/marklubin/researchguy/internal/config"
)

func defaultTestConfig() *config.Config {
	return &config.Config{
		DefaultBackend: "claude",
		Claude: config.ClaudeConfig{
			Binary:    "claude",
			Model:     "opus",
			MaxTokens: 16000,
		},
		Ollama: config.OllamaConfig{
			Host:         "http://localhost:11434",
			Model:        "glm-4.7-flash",
			UtilityModel: "qwen3.5:9b",
			NumCtx:       32768,
			NumPredict:   4096,
			KeepAlive:    "0s",
		},
		Hybrid: config.HybridConfig{
			WorkerBackend:      "ollama",
			WorkerModels:       []string{"glm-4.7-flash"},
			AggregatorBackend:  "ollama",
			AggregatorModel:    "qwen3.8:27b-q4_K_M",
			VerifierBackend:    "ollama",
			VerifierModel:      "qwen3.5:9b",
			EnableVerification: false,
			MaxParallel:        1,
		},
		Tools: config.ToolsConfig{
			Enabled:       true,
			MaxIterations: 6,
			MaxResults:    6,
		},
	}
}

func TestNewProvider_Claude(t *testing.T) {
	cfg := defaultTestConfig()
	p, err := NewProvider(cfg, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c, ok := p.(*Claude)
	if !ok {
		t.Fatalf("expected *Claude, got %T", p)
	}
	if c.Model != "opus" {
		t.Errorf("Model = %q, want %q", c.Model, "opus")
	}
}

func TestNewProvider_Ollama(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.DefaultBackend = "ollama"

	p, err := NewProvider(cfg, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	o, ok := p.(*Ollama)
	if !ok {
		t.Fatalf("expected *Ollama, got %T", p)
	}
	if o.Model != "glm-4.7-flash" {
		t.Errorf("Model = %q, want %q", o.Model, "glm-4.7-flash")
	}
	if o.Executor == nil {
		t.Error("Executor should not be nil")
	}
	if o.NumCtx != 32768 || o.NumPredict != 4096 || o.KeepAlive != "0s" {
		t.Errorf("resource controls = %+v", o)
	}
}

func TestNewProvider_Override(t *testing.T) {
	cfg := defaultTestConfig()

	t.Run("backend override", func(t *testing.T) {
		p, err := NewProvider(cfg, "ollama", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := p.(*Ollama); !ok {
			t.Errorf("expected *Ollama with backend override, got %T", p)
		}
	})

	t.Run("model override", func(t *testing.T) {
		p, err := NewProvider(cfg, "", "sonnet")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		c, ok := p.(*Claude)
		if !ok {
			t.Fatalf("expected *Claude, got %T", p)
		}
		if c.Model != "sonnet" {
			t.Errorf("Model = %q, want %q", c.Model, "sonnet")
		}
	})
}

func TestNewProvider_OllamaModelOverride(t *testing.T) {
	cfg := defaultTestConfig()
	p, err := NewProvider(cfg, "ollama", "custom-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	o, ok := p.(*Ollama)
	if !ok {
		t.Fatalf("expected *Ollama, got %T", p)
	}
	if o.Model != "custom-model" {
		t.Errorf("Model = %q, want %q", o.Model, "custom-model")
	}
}

func TestNewProvider_Hybrid(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.DefaultBackend = "hybrid"

	p, err := NewProvider(cfg, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h, ok := p.(*Hybrid)
	if !ok {
		t.Fatalf("expected *Hybrid, got %T", p)
	}
	if h.WorkerBackend != "ollama" {
		t.Errorf("WorkerBackend = %q, want %q", h.WorkerBackend, "ollama")
	}
	if h.AggregatorBackend != "ollama" {
		t.Errorf("AggregatorBackend = %q, want %q", h.AggregatorBackend, "ollama")
	}
	if h.AggregatorModel != "qwen3.8:27b-q4_K_M" {
		t.Errorf("AggregatorModel = %q, want %q", h.AggregatorModel, "qwen3.8:27b-q4_K_M")
	}
	if h.EnableVerification {
		t.Error("EnableVerification should be false")
	}
}

func TestNewUtilityProvider_UsesDedicatedModelWithoutFallback(t *testing.T) {
	cfg := defaultTestConfig()
	p, err := NewUtilityProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	o, ok := p.(*Ollama)
	if !ok {
		t.Fatalf("expected *Ollama, got %T", p)
	}
	if o.Model != "qwen3.5:9b" {
		t.Errorf("Model = %q, want qwen3.5:9b", o.Model)
	}
	if o.FallbackModel != "" {
		t.Errorf("FallbackModel = %q, want empty", o.FallbackModel)
	}
}

func TestNewProvider_Unknown(t *testing.T) {
	cfg := defaultTestConfig()
	_, err := NewProvider(cfg, "unknown", "")
	if err == nil {
		t.Fatal("expected error for unknown backend")
	}
}
