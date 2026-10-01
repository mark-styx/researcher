package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/tools"
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

// EvidenceRecord is source material returned by a tool call during a model
// run. It deliberately excludes the model's interpretation of that material.
type EvidenceRecord struct {
	Label   string `json:"label"`
	Content string `json:"content"`
}

// EvidenceProvider exposes the raw tool results used during the last run.
type EvidenceProvider interface {
	Evidence() []EvidenceRecord
}

// Unloader lets staged providers release model memory before the next stage.
type Unloader interface {
	Unload(ctx context.Context) error
}

// Request holds the parameters for an LLM completion.
type Request struct {
	SystemPrompt string
	UserPrompt   string
	MaxTokens    int
	Tools        []tools.Tool // nil = no tools

	// Mode and BranchCount only affect the hybrid backend's shard planning.
	// Other backends ignore them.
	Mode        string // "" (general), "landscape", or "inquiry" — selects the epistemic branch-role set
	BranchCount int    // number of shards to plan; <= 0 defaults to len(worker models)
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
		return newOllamaProvider(cfg, model, cfg.Ollama.FallbackModel), nil

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

// NewUtilityProvider creates the small, single-model Ollama provider used for
// bounded utility work such as report categorization. A blank utility model
// means callers should keep using their existing provider for compatibility.
func NewUtilityProvider(cfg *config.Config) (Provider, error) {
	model := strings.TrimSpace(cfg.Ollama.UtilityModel)
	if model == "" {
		return nil, nil
	}
	return newOllamaProvider(cfg, model, ""), nil
}

func newOllamaProvider(cfg *config.Config, model, fallback string) *Ollama {
	return &Ollama{
		Host:          cfg.Ollama.Host,
		Model:         model,
		FallbackModel: fallback,
		NumCtx:        cfg.Ollama.NumCtx,
		NumPredict:    cfg.Ollama.NumPredict,
		KeepAlive:     cfg.Ollama.KeepAlive,
		MaxIterations: cfg.Tools.MaxIterations,
		Executor:      tools.NewExecutor(cfg.Tools.MaxResults),
	}
}
