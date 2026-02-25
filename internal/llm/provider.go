package llm

import (
	"context"
	"fmt"

	"github.com/marklubin/researcher/internal/config"
)

// Provider is the interface for LLM backends.
type Provider interface {
	Complete(ctx context.Context, req Request) (string, error)
	Name() string
}

// Request holds the parameters for an LLM completion.
type Request struct {
	SystemPrompt string
	UserPrompt   string
	MaxTokens    int
}

// NewProvider creates an LLM provider based on config and optional overrides.
func NewProvider(cfg *config.Config, backendOverride, modelOverride string) (Provider, error) {
	backend := cfg.DefaultBackend
	if backendOverride != "" {
		backend = backendOverride
	}

	switch backend {
	case "claude":
		model := cfg.Claude.Model
		if modelOverride != "" {
			model = modelOverride
		}
		return &Claude{
			Binary:    cfg.Claude.Binary,
			Model:     model,
			MaxTokens: cfg.Claude.MaxTokens,
		}, nil

	case "ollama":
		model := cfg.Ollama.Model
		if modelOverride != "" {
			model = modelOverride
		}
		return &Ollama{
			Host:          cfg.Ollama.Host,
			Model:         model,
			FallbackModel: cfg.Ollama.FallbackModel,
		}, nil

	default:
		return nil, fmt.Errorf("unknown backend: %q (expected claude or ollama)", backend)
	}
}
