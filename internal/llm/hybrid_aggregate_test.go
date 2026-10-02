package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/critique"
)

// aggregateHybrid is a one-worker hybrid with verification on and the
// aggregator on aggBackend.
func aggregateHybrid(aggBackend string) (h *Hybrid, worker, aggregator, verifier *stubProvider) {
	worker = &stubProvider{name: "worker", resp: "worker draft",
		evidence: []EvidenceRecord{{Label: "web_search query=cats", Content: "ledger snippet"}}}
	aggregator = &stubProvider{name: aggBackend, resp: reportBegin + "\nCats are \"small\" [P:9].\n" + reportEnd}
	verifier = &stubProvider{name: "claude", responses: []string{critique.NoUnsupportedClaims, critique.NoNarrativeClaims}}
	h = &Hybrid{
		cfg:                &config.Config{Claude: config.ClaudeConfig{Model: "opus"}},
		WorkerBackend:      "ollama",
		WorkerModels:       []string{"worker"},
		AggregatorBackend:  aggBackend,
		AggregatorModel:    "agg",
		VerifierBackend:    "claude",
		VerifierModel:      "sonnet",
		EnableVerification: true,
		MaxParallel:        1,
		makeProvider: func(backend, model string) (Provider, error) {
			switch backend + "/" + model {
			case "ollama/worker":
				return worker, nil
			case aggBackend + "/agg":
				return aggregator, nil
			case "claude/sonnet":
				return verifier, nil
			}
			return nil, fmt.Errorf("unexpected provider %s/%s", backend, model)
		},
	}
	return h, worker, aggregator, verifier
}

var readProfile = []MCPServer{{Name: "researchguy", Command: "researchguy", Args: []string{"mcp", "--profile", "read"}}}

func TestHybrid_BeforeAggregateFeedsAggregatorAndCritics(t *testing.T) {
	h, worker, aggregator, verifier := aggregateHybrid("claude")
	var workerDone bool
	var citedDraft string
	req := Request{UserPrompt: "cats", MCP: readProfile, BeforeAggregate: func(context.Context) (AggregateInput, error) {
		workerDone = worker.calls == 1
		return AggregateInput{RunID: "run-1", Sources: "| [S:5] | zoo.example | Cats |\n", MCP: readProfile,
			Cited: func(_ context.Context, draft string) []critique.Evidence {
				citedDraft = draft
				return []critique.Evidence{{ID: "P:9", Label: "zoo.example", Content: "cited passage text"}}
			}}, nil
	}}
	if _, err := h.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !workerDone {
		t.Error("BeforeAggregate ran before the workers finished")
	}
	if worker.lastReq.MCP != nil || worker.lastReq.BeforeAggregate != nil {
		t.Error("worker got the aggregator's MCP servers or hook")
	}
	prompt := aggregator.lastReq.UserPrompt
	for _, want := range []string{"| [S:5] | zoo.example | Cats |", `run_id "run-1"`, "[P:<id>]", "ledger snippet"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("aggregator prompt missing %q", want)
		}
	}
	if len(aggregator.lastReq.MCP) != 1 || aggregator.lastReq.MCP[0].Name != "researchguy" {
		t.Errorf("aggregator MCP = %+v", aggregator.lastReq.MCP)
	}
	if !strings.Contains(citedDraft, "[P:9]") || !strings.Contains(verifier.lastReq.UserPrompt, "cited passage text") {
		t.Errorf("critic didn't get the cited passage: draft %q", citedDraft)
	}
	meta := h.Metadata()
	for _, want := range []string{`"sources_table":true`, `"aggregator_tools":true`, `"cited_passages":1`} {
		if !strings.Contains(meta, want) {
			t.Errorf("metadata missing %s: %s", want, meta)
		}
	}
}

func TestHybrid_BeforeAggregateErrorAggregatesFromLedger(t *testing.T) {
	h, _, aggregator, _ := aggregateHybrid("claude")
	req := Request{UserPrompt: "cats", BeforeAggregate: func(context.Context) (AggregateInput, error) {
		return AggregateInput{Sources: "ignored", MCP: readProfile}, errors.New("index down")
	}}
	if _, err := h.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	prompt := aggregator.lastReq.UserPrompt
	if strings.Contains(prompt, "researchguy tools") || strings.Contains(prompt, "ignored") || aggregator.lastReq.MCP != nil {
		t.Errorf("failed hook still fed the aggregator: %s", prompt)
	}
	if !strings.Contains(prompt, "ledger snippet") || !strings.Contains(h.Metadata(), `"before_aggregate_error":"index down"`) {
		t.Errorf("metadata = %s", h.Metadata())
	}
}

func TestHybrid_NonClaudeAggregatorGetsNoTools(t *testing.T) {
	h, _, aggregator, _ := aggregateHybrid("ollama")
	req := Request{UserPrompt: "cats", BeforeAggregate: func(context.Context) (AggregateInput, error) {
		return AggregateInput{RunID: "run-1", Sources: "| [S:5] |\n", MCP: readProfile}, nil
	}}
	if _, err := h.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	prompt := aggregator.lastReq.UserPrompt
	if aggregator.lastReq.MCP != nil || strings.Contains(prompt, "researchguy tools") {
		t.Error("ollama aggregator was offered MCP tools")
	}
	if !strings.Contains(prompt, "| [S:5] |") || !strings.Contains(prompt, "[S:<id>]") {
		t.Errorf("sources table missing: %s", prompt)
	}
}
