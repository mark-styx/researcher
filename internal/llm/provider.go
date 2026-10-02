package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/store"
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
	// ID is the ledger ID (E<seq>) the record got when Request.Capture
	// wrote it to the run, "" when nothing captured it.
	ID      string `json:"id,omitempty"`
	Label   string `json:"label"`
	Content string `json:"content"`
	// Call is the tool call, kept for the run record.
	store.Call
}

// CaptureFunc writes one tool result to the run record as it arrives and
// returns its ledger ID.
type CaptureFunc func(EvidenceRecord) string

// capture hands rec to req.Capture, if set, and returns rec with its ID.
func (req Request) capture(rec EvidenceRecord) EvidenceRecord {
	if req.Capture != nil && rec.ID == "" {
		rec.ID = req.Capture(rec)
	}
	return rec
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

	// Run is the store run this call belongs to. The hybrid backend writes
	// its captures, worker drafts and aggregator prompt and output there.
	// nil records nothing.
	Run *store.Run

	// Capture, when set, is called with each tool result as it arrives,
	// before the model continues, so a provider that dies mid-run has
	// already recorded what it collected. Providers that report Evidence
	// call it; the hybrid backend sets it for each worker.
	Capture CaptureFunc
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
			Binary:           cfg.Claude.Binary,
			Model:            model,
			MaxBudgetUSD:     cfg.Claude.MaxBudgetUSD,
			MaxTurns:         cfg.Claude.MaxTurns,
			IgnoreUserConfig: cfg.Claude.IgnoreUserConfig,
		}, nil

	case "ollama":
		model := cfg.Ollama.Model
		if modelOverride != "" {
			model = modelOverride
		}
		return newOllamaProvider(cfg, model, cfg.Ollama.FallbackModel), nil

	case "codex":
		model := cfg.Codex.Model
		if modelOverride != "" {
			model = modelOverride
		}
		return &Codex{
			Binary:           cfg.Codex.Binary,
			Model:            model,
			ReasoningEffort:  cfg.Codex.ReasoningEffort,
			IgnoreUserConfig: cfg.Codex.IgnoreUserConfig,
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
			MaxEvidenceChars:   cfg.Hybrid.MaxEvidenceChars,
		}
		h.makeProvider = func(backend, model string) (Provider, error) {
			return NewProvider(cfg, backend, model)
		}
		return h, nil

	default:
		return nil, fmt.Errorf("unknown backend: %q (expected claude, ollama, codex, or hybrid)", backend)
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
