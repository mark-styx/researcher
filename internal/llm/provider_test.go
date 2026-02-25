package llm

import (
	"testing"

	"github.com/marklubin/researcher/internal/config"
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
			Host:          "http://localhost:11434",
			Model:         "qwen3-coder-next",
			FallbackModel: "nemotron",
		},
		Tools: config.ToolsConfig{
			Enabled:       true,
			MaxIterations: 20,
			MaxResults:    10,
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
	if o.Model != "qwen3-coder-next" {
		t.Errorf("Model = %q, want %q", o.Model, "qwen3-coder-next")
	}
	if o.Executor == nil {
		t.Error("Executor should not be nil")
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

func TestNewProvider_Unknown(t *testing.T) {
	cfg := defaultTestConfig()
	_, err := NewProvider(cfg, "unknown", "")
	if err == nil {
		t.Fatal("expected error for unknown backend")
	}
}
