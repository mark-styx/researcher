package llm

import (
	"context"
	"fmt"

	"github.com/marklubin/researcher/internal/config"
	"github.com/marklubin/researcher/internal/tools"
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
	Tools        []tools.Tool // nil = no tools
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
			Binary:       cfg.Claude.Binary,
			Model:        model,
			MaxBudgetUSD: cfg.Claude.MaxBudgetUSD,
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
			MaxIterations: cfg.Tools.MaxIterations,
			Executor:      tools.NewExecutor(cfg.Tools.MaxResults),
		}, nil

	default:
		return nil, fmt.Errorf("unknown backend: %q (expected claude or ollama)", backend)
	}
}
