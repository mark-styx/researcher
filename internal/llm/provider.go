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

// MetadataProvider is an optional extension that exposes last-run metadata.
type MetadataProvider interface {
	Metadata() string
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
			MaxTurns:     cfg.Claude.MaxTurns,
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

	case "hybrid":
		aggregatorModel := cfg.Hybrid.AggregatorModel
		if modelOverride != "" {
			aggregatorModel = modelOverride
		}
		h := &Hybrid{
			cfg:                cfg,
			WorkerBackend:      cfg.Hybrid.WorkerBackend,
			WorkerModels:       cfg.Hybrid.WorkerModels,
			AggregatorBackend:  cfg.Hybrid.AggregatorBackend,
			AggregatorModel:    aggregatorModel,
			VerifierBackend:    cfg.Hybrid.VerifierBackend,
			VerifierModel:      cfg.Hybrid.VerifierModel,
			EnableVerification: cfg.Hybrid.EnableVerification,
			MaxParallel:        cfg.Hybrid.MaxParallel,
		}
		h.makeProvider = func(backend, model string) (Provider, error) {
			return NewProvider(cfg, backend, model)
		}
		return h, nil

	default:
		return nil, fmt.Errorf("unknown backend: %q (expected claude, ollama, or hybrid)", backend)
	}
}
