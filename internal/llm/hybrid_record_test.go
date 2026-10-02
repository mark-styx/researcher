package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/store"
)

func TestExtractReport(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		want       string
		markers    string
		discarded  int
		reportSize int
	}{
		{
			name:      "chatter around markers is dropped",
			raw:       "I'll write the report now.\n===BEGIN REPORT===\n# Title\n\nBody [E3].\n===END REPORT===\nWant me to save it to a file?",
			want:      "# Title\n\nBody [E3].",
			markers:   markersOK,
			discarded: len("I'll write the report now.") + len("Want me to save it to a file?"),
		},
		{
			name:    "decorated markers",
			raw:     "**===BEGIN REPORT===**\nbody\n`===END REPORT===`",
			want:    "body",
			markers: markersOK,
		},
		{
			name:    "first begin and last end win",
			raw:     "===BEGIN REPORT===\na\n===END REPORT===\nb\n===END REPORT===",
			want:    "a\n===END REPORT===\nb",
			markers: markersOK,
		},
		{
			name:      "unterminated keeps everything after begin",
			raw:       "preamble\n===BEGIN REPORT===\nbody cut off",
			want:      "body cut off",
			markers:   markersUnterminated,
			discarded: len("preamble"),
		},
		{
			name:    "missing markers keeps the whole output",
			raw:     "  just a report  \n",
			want:    "just a report",
			markers: markersMissing,
		},
		{
			name:    "end without begin counts as missing",
			raw:     "body\n===END REPORT===",
			want:    "body\n===END REPORT===",
			markers: markersMissing,
		},
		{
			name:    "nothing between markers keeps the whole output",
			raw:     "note\n===BEGIN REPORT===\n\n===END REPORT===",
			want:    "note\n===BEGIN REPORT===\n\n===END REPORT===",
			markers: markersEmpty,
		},
		{
			name:    "empty output",
			raw:     "",
			want:    "",
			markers: markersMissing,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, out := extractReport(tc.raw)
			if got != tc.want {
				t.Errorf("report = %q, want %q", got, tc.want)
			}
			if out.Markers != tc.markers || out.DiscardedChars != tc.discarded || out.ReportChars != len(tc.want) {
				t.Errorf("output = %+v, want markers %q, discarded %d, report %d", out, tc.markers, tc.discarded, len(tc.want))
			}
		})
	}
}

func TestAssignEvidenceIDs_NumbersAcrossWorkersIncludingFailed(t *testing.T) {
	workers := []hybridWorkerOutput{
		shardWorker(0, "a", 2, 10),
		{Index: 1, Shard: "failed", Err: errors.New("boom"), Evidence: shardWorker(1, "failed", 1, 10).Evidence},
		{Index: 2, Shard: "none"},
		shardWorker(3, "b", 2, 10),
	}
	assignEvidenceIDs(nil, workers)
	var ids []string
	for _, w := range workers {
		for _, e := range w.Evidence {
			ids = append(ids, e.ID)
		}
	}
	if got := strings.Join(ids, ","); got != "E1,E2,E3,E4,E5" {
		t.Errorf("ids = %s, want E1..E5 in worker order", got)
	}

	// The aggregator prompt cites those IDs, skipping the failed worker's E3.
	ev, _ := buildLedger(workers, 1_000)
	prompt := buildAggregationPrompt(Request{UserPrompt: "topic"}, workers, ev, AggregateInput{})
	for _, want := range []string{"[E1 | shard: a | item 0]", "[E2 | shard: a | item 1]", "[E4 | shard: b | item 0]", "[E5 | shard: b | item 1]"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("aggregator prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "[E3 |") {
		t.Error("failed worker's evidence reached the aggregator")
	}
	if !strings.Contains(prompt, reportBegin) || !strings.Contains(aggregationSystemPrompt(""), reportEnd) {
		t.Error("aggregator prompts don't state the report markers")
	}
}

// recordingHybrid builds a hybrid whose workers each return items x size
// chars of evidence and whose aggregator returns aggResp (or aggErr).
func recordingHybrid(cfg *config.Config, items, size int, aggregator *stubProvider) *Hybrid {
	return &Hybrid{
		cfg:               cfg,
		WorkerBackend:     "codex",
		WorkerModels:      []string{"w"},
		AggregatorBackend: "claude",
		AggregatorModel:   "opus",
		MaxParallel:       5,
		makeProvider: func(backend, model string) (Provider, error) {
			if backend == "claude" || backend == "ollama" {
				return aggregator, nil
			}
			ev := make([]EvidenceRecord, items)
			for i := range ev {
				ev[i] = EvidenceRecord{Label: fmt.Sprintf("web_search: q%d", i), Content: strings.Repeat("x", size)}
			}
			return &stubProvider{name: "codex", resp: "worker draft", evidence: ev}, nil
		},
	}
}

func openRun(t *testing.T) *store.Run {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.StartRun(store.RunRecord{Kind: "dive", Topic: "topic"})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestHybridComplete_RecordsFullLedgerEvenWhenPromptIsCapped(t *testing.T) {
	cfg := &config.Config{Claude: config.ClaudeConfig{Model: "opus"}, Hybrid: config.HybridConfig{MaxEvidenceChars: 10_000}}
	aggregator := &stubProvider{name: "claude", resp: "Sure, here it is.\n===BEGIN REPORT===\n# Report\n\nFinding [E1].\n===END REPORT===\nShall I save it?"}
	h := recordingHybrid(cfg, 4, 1_000, aggregator)
	run := openRun(t)

	got, err := h.Complete(context.Background(), Request{UserPrompt: "topic", Mode: "inquiry", Run: run})
	if err != nil {
		t.Fatal(err)
	}
	if got != "# Report\n\nFinding [E1]." {
		t.Errorf("report = %q, want only the text between the markers", got)
	}

	// 5 shards x 4 items x 1k = 20k captured, all on disk, though the
	// prompt got 10k.
	captures, err := store.ReadCaptures(run.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if len(captures) != 20 {
		t.Fatalf("captured %d items, want all 20", len(captures))
	}
	for i, c := range captures {
		if c.Seq != i+1 || len(c.Content) != 1_000 || c.Worker < 1 || c.Shard == "" || c.Backend != "codex" {
			t.Errorf("capture %d = seq %d worker %d shard %q backend %q, %d chars", i, c.Seq, c.Worker, c.Shard, c.Backend, len(c.Content))
		}
	}
	prompt := aggregator.lastReq.UserPrompt
	for _, c := range captures {
		id := fmt.Sprintf("[E%d | ", c.Seq)
		if strings.Contains(prompt, id) && !strings.Contains(prompt, id+c.Label+"]") {
			t.Errorf("prompt cites %s with a label other than capture %d's %q", id, c.Seq, c.Label)
		}
	}

	files := map[string]string{}
	for _, name := range []string{"workers.jsonl", "aggregator-prompt.md", "aggregator-output.md"} {
		data, err := os.ReadFile(filepath.Join(run.Dir(), name))
		if err != nil {
			t.Fatalf("run record missing %s: %v", name, err)
		}
		files[name] = string(data)
	}
	if n := strings.Count(files["workers.jsonl"], "\n"); n != 5 {
		t.Errorf("workers.jsonl has %d lines, want 5", n)
	}
	if !strings.Contains(files["workers.jsonl"], `"content":"worker draft"`) {
		t.Error("workers.jsonl doesn't hold the full worker drafts")
	}
	if !strings.Contains(files["aggregator-prompt.md"], prompt) {
		t.Error("aggregator-prompt.md doesn't hold the prompt the aggregator got")
	}
	if !strings.Contains(files["aggregator-output.md"], "Shall I save it?") {
		t.Error("aggregator-output.md should keep the raw output, chatter included")
	}

	var meta struct {
		Ledger ledgerStats      `json:"evidence_ledger"`
		Output aggregatorOutput `json:"aggregator_output"`
		Errors []string         `json:"store_errors"`
	}
	if err := json.Unmarshal([]byte(h.Metadata()), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Ledger.ItemsCaptured != 20 || meta.Ledger.CharsPassed != 10_000 {
		t.Errorf("ledger = %+v", meta.Ledger)
	}
	if meta.Output.Markers != markersOK || meta.Output.DiscardedChars == 0 {
		t.Errorf("aggregator output = %+v", meta.Output)
	}
	if len(meta.Errors) != 0 {
		t.Errorf("store errors = %v", meta.Errors)
	}
}

func TestHybridComplete_ClaudeAggregatorGetsWholeLedgerByDefault(t *testing.T) {
	cfg := &config.Config{Claude: config.ClaudeConfig{Model: "opus"}}
	aggregator := &stubProvider{name: "claude", resp: "===BEGIN REPORT===\nr\n===END REPORT==="}
	// 5 shards x 30 items x 6k = 900k chars, about what a 2026-10-02 dive
	// captured, and more than the old 400k cap.
	h := recordingHybrid(cfg, 30, 6_000, aggregator)
	if _, err := h.Complete(context.Background(), Request{UserPrompt: "topic", Mode: "inquiry"}); err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Ledger ledgerStats `json:"evidence_ledger"`
	}
	if err := json.Unmarshal([]byte(h.Metadata()), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Ledger.MaxChars != claudeEvidenceChars || meta.Ledger.CharsPassed != 900_000 || meta.Ledger.ItemsPassed != 150 {
		t.Errorf("ledger = %+v, want all 900000 chars passed under the claude default", meta.Ledger)
	}
}

func TestHybridComplete_AggregatorFailureStillLeavesEvidenceOnDisk(t *testing.T) {
	cfg := &config.Config{Claude: config.ClaudeConfig{Model: "opus"}}
	aggregator := &stubProvider{name: "claude", err: errors.New("prompt is too long")}
	h := recordingHybrid(cfg, 2, 100, aggregator)
	run := openRun(t)

	if _, err := h.Complete(context.Background(), Request{UserPrompt: "topic", Mode: "inquiry", Run: run}); err == nil {
		t.Fatal("want the aggregator error")
	}
	captures, err := store.ReadCaptures(run.Dir())
	if err != nil || len(captures) != 10 {
		t.Errorf("captures = %d (%v), want all 10 written before aggregation", len(captures), err)
	}
	if _, err := os.Stat(filepath.Join(run.Dir(), "aggregator-prompt.md")); err != nil {
		t.Errorf("aggregator prompt not saved: %v", err)
	}
	if h.Metadata() == "" {
		t.Error("metadata should describe the workers and ledger even when aggregation fails")
	}
}

func TestHybridComplete_AllWorkersFailedStillRecordsThem(t *testing.T) {
	cfg := &config.Config{Claude: config.ClaudeConfig{Model: "opus"}}
	run := openRun(t)
	h := &Hybrid{
		cfg: cfg, WorkerBackend: "codex", WorkerModels: []string{"w"}, AggregatorBackend: "claude", MaxParallel: 2,
		makeProvider: func(backend, model string) (Provider, error) {
			return &stubProvider{name: "codex", err: errors.New("quota"), evidence: []EvidenceRecord{{Label: "web_search: q", Content: "partial"}}}, nil
		},
	}
	if _, err := h.Complete(context.Background(), Request{UserPrompt: "topic", BranchCount: 2, Run: run}); err == nil {
		t.Fatal("want an error when every worker fails")
	}
	captures, _ := store.ReadCaptures(run.Dir())
	if len(captures) != 2 || captures[0].WorkerError != "quota" {
		t.Errorf("captures = %+v, want the failed workers' partial evidence kept with their error", captures)
	}
	if _, err := os.Stat(filepath.Join(run.Dir(), "workers.jsonl")); err != nil {
		t.Errorf("workers.jsonl not written: %v", err)
	}
}

func TestHybridComplete_StoreWriteFailureIsReportedNotFatal(t *testing.T) {
	cfg := &config.Config{Claude: config.ClaudeConfig{Model: "opus"}}
	aggregator := &stubProvider{name: "claude", resp: "===BEGIN REPORT===\nr\n===END REPORT==="}
	h := recordingHybrid(cfg, 1, 10, aggregator)
	run := openRun(t)
	if err := os.RemoveAll(run.Dir()); err != nil {
		t.Fatal(err)
	}

	got, err := h.Complete(context.Background(), Request{UserPrompt: "topic", BranchCount: 1, Run: run})
	if err != nil || got != "r" {
		t.Fatalf("Complete() = %q, %v; a broken store shouldn't stop the report", got, err)
	}
	var meta struct {
		Errors []string `json:"store_errors"`
	}
	if err := json.Unmarshal([]byte(h.Metadata()), &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.Errors) == 0 {
		t.Error("store write failures should be listed in store_errors")
	}
}

func TestHybridComplete_CriticWithSmallerBudgetGetsItsOwnLedger(t *testing.T) {
	cfg := &config.Config{Claude: config.ClaudeConfig{Model: "opus"}}
	var criticPrompts []string
	aggregator := &stubProvider{name: "claude", resp: "===BEGIN REPORT===\nreport\n===END REPORT==="}
	critic := &stubProvider{name: "ollama", resp: "No unsupported claims found.", record: func(p string) { criticPrompts = append(criticPrompts, p) }}
	h := recordingHybrid(cfg, 30, 1_000, aggregator)
	h.EnableVerification = true
	h.VerifierBackend = "ollama"
	h.VerifierModel = "small"
	base := h.makeProvider
	h.makeProvider = func(backend, model string) (Provider, error) {
		if backend == "ollama" {
			return critic, nil
		}
		return base(backend, model)
	}

	if _, err := h.Complete(context.Background(), Request{UserPrompt: "topic", Mode: "inquiry"}); err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Ledger       ledgerStats  `json:"evidence_ledger"`
		CriticLedger *ledgerStats `json:"critic_evidence_ledger"`
	}
	if err := json.Unmarshal([]byte(h.Metadata()), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Ledger.CharsPassed != 150_000 {
		t.Errorf("aggregator ledger passed %d chars, want all 150000", meta.Ledger.CharsPassed)
	}
	if meta.CriticLedger == nil || meta.CriticLedger.MaxChars != defaultHybridEvidenceChars || meta.CriticLedger.CharsPassed > defaultHybridEvidenceChars {
		t.Errorf("critic ledger = %+v, want one capped at %d", meta.CriticLedger, defaultHybridEvidenceChars)
	}
	if len(criticPrompts) != 2 || len(criticPrompts[0]) > defaultHybridEvidenceChars+20_000 {
		t.Errorf("critics got %d prompts, first %d chars; want 2 sized to the ollama budget", len(criticPrompts), len(criticPrompts[0]))
	}
}

func TestPromptRecord(t *testing.T) {
	got := string(promptRecord(Request{SystemPrompt: "sys", UserPrompt: "user"}))
	if got != "# System prompt\n\nsys\n\n# User prompt\n\nuser\n" {
		t.Errorf("promptRecord = %q", got)
	}
}

// Evidence IDs must survive the fair-share cut, or a truncated item would
// be cited under a number that doesn't match the run record.
func TestBuildLedger_KeepsIDs(t *testing.T) {
	w := shardWorker(0, "a", 3, 100)
	assignEvidenceIDs(nil, []hybridWorkerOutput{w})
	ev, _ := buildLedger([]hybridWorkerOutput{w}, 150)
	if len(ev) != 2 || ev[0].ID != "E1" || ev[1].ID != "E2" {
		t.Errorf("ledger = %+v, want E1 whole and E2 cut", ev)
	}
}

// streamingStub captures each record through req.Capture during Complete,
// as the codex and ollama providers do, then fails if err is set.
type streamingStub struct {
	records []EvidenceRecord
	err     error
	mu      sync.Mutex
	got     []EvidenceRecord
}

func (s *streamingStub) Name() string { return "codex" }

func (s *streamingStub) Complete(_ context.Context, req Request) (string, error) {
	var got []EvidenceRecord
	for _, r := range s.records {
		got = append(got, req.capture(r))
	}
	s.mu.Lock()
	s.got = got
	s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	return "worker draft", nil
}

func (s *streamingStub) Evidence() []EvidenceRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]EvidenceRecord(nil), s.got...)
}

func TestHybridComplete_StreamedCapturesKeepTheirIDs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		streamErr error
	}{
		{"streaming worker succeeds", nil},
		{"streaming worker fails after capturing", errors.New("codex died")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aggregator := &stubProvider{name: "claude", resp: "===BEGIN REPORT===\nreport\n===END REPORT==="}
			h := &Hybrid{
				cfg:               &config.Config{Claude: config.ClaudeConfig{Model: "opus"}},
				WorkerBackend:     "codex",
				WorkerModels:      []string{"stream", "plain"},
				AggregatorBackend: "claude",
				AggregatorModel:   "opus",
				MaxParallel:       2,
				makeProvider: func(backend, model string) (Provider, error) {
					switch model {
					case "stream":
						return &streamingStub{err: tc.streamErr, records: []EvidenceRecord{
							{Label: "web_search: a", Content: "results a", Call: store.Call{Tool: "web_search", Action: "search", Query: "a",
								Results: []store.CaptureResult{{Rank: 1, URL: "https://a.test/"}}}},
							{Label: "web_search open: https://a.test/", Content: "page a", Call: store.Call{Tool: "web_search", Action: "open", URL: "https://a.test/"}},
						}}, nil
					case "plain":
						return &stubProvider{name: "codex", resp: "worker draft", evidence: []EvidenceRecord{{Label: "web_search: b", Content: "results b"}}}, nil
					}
					return aggregator, nil
				},
			}
			run := openRun(t)
			if _, err := h.Complete(context.Background(), Request{UserPrompt: "topic", Run: run}); err != nil {
				t.Fatal(err)
			}
			captures, err := store.ReadCaptures(run.Dir())
			if err != nil {
				t.Fatal(err)
			}
			if len(captures) != 3 {
				t.Fatalf("captures = %d, want 3", len(captures))
			}
			// Streamed captures are written during the worker's call, so
			// they come before the one written after the workers finish.
			a, open, b := captures[0], captures[1], captures[2]
			if a.Seq != 1 || a.Worker != 1 || a.Action != "search" || len(a.Results) != 1 || !strings.HasPrefix(a.Label, "codex/stream | shard: ") {
				t.Errorf("capture 1 = %+v", a)
			}
			if open.Seq != 2 || open.Action != "open" || open.URL != "https://a.test/" {
				t.Errorf("capture 2 = %+v", open)
			}
			if b.Seq != 3 || b.Worker != 2 || !strings.HasPrefix(b.Label, "codex/plain | shard: ") {
				t.Errorf("capture 3 = %+v", b)
			}
			prompt := aggregator.lastReq.UserPrompt
			if !strings.Contains(prompt, "[E3 | "+b.Label+"]") {
				t.Errorf("aggregator prompt doesn't cite the plain worker's item as E3")
			}
			streamedCited := strings.Contains(prompt, "[E1 | "+a.Label+"]") && strings.Contains(prompt, "[E2 | "+open.Label+"]")
			if tc.streamErr == nil && !streamedCited {
				t.Error("aggregator prompt doesn't cite the streamed items by their capture seqs")
			}
			if tc.streamErr != nil && strings.Contains(prompt, "[E1 |") {
				t.Error("a failed worker's evidence reached the aggregator")
			}
		})
	}
}
