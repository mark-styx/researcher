package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/critique"
)

// shardWorker returns a successful worker output whose evidence is n items of
// size chars each, labeled with the shard so tests can tell them apart.
func shardWorker(idx int, shard string, n, size int) hybridWorkerOutput {
	ev := make([]critique.Evidence, n)
	for i := range ev {
		ev[i] = critique.Evidence{
			Label:   fmt.Sprintf("shard: %s | item %d", shard, i),
			Content: strings.Repeat("x", size),
		}
	}
	return hybridWorkerOutput{Index: idx, Shard: shard, Content: "prose", Evidence: ev}
}

func ledgerChars(ev []critique.Evidence) map[string]int {
	out := map[string]int{}
	for _, e := range ev {
		shard := strings.SplitN(strings.TrimPrefix(e.Label, "shard: "), " |", 2)[0]
		out[shard] += len(e.Content)
	}
	return out
}

// Regression: the cap used to fill in shard order, so with search-heavy
// workers the first shard took the whole ledger and the rest got nothing.
func TestWorkerEvidence_EveryShardGetsAShareOfTheCap(t *testing.T) {
	var workers []hybridWorkerOutput
	for i, shard := range branchSets["inquiry"] {
		workers = append(workers, shardWorker(i, shard, 30, 5_000))
	}

	got := ledgerChars(workerEvidence(workers, 80_000))
	for _, shard := range branchSets["inquiry"] {
		if got[shard] != 16_000 {
			t.Errorf("shard %q got %d chars of the ledger, want an equal 16000 share", shard, got[shard])
		}
	}
}

func TestWorkerEvidence_UnusedShareGoesToShardsThatNeedIt(t *testing.T) {
	workers := []hybridWorkerOutput{
		shardWorker(0, "big-a", 10, 10_000), // needs 100k
		shardWorker(1, "small", 1, 2_000),   // needs 2k
		shardWorker(2, "big-b", 10, 10_000), // needs 100k
		shardWorker(3, "medium", 3, 5_000),  // needs 15k
	}

	got := ledgerChars(workerEvidence(workers, 100_000))
	// 100k / 4 = 25k each; small and medium fit under that, so their unused
	// 23k + 10k is split between the two big shards: (100k - 17k) / 2.
	want := map[string]int{"big-a": 41_500, "small": 2_000, "big-b": 41_500, "medium": 15_000}
	for shard, w := range want {
		if got[shard] != w {
			t.Errorf("shard %q got %d chars, want %d", shard, got[shard], w)
		}
	}
	total := 0
	for _, n := range got {
		total += n
	}
	if total > 100_000 {
		t.Errorf("ledger total %d exceeds the 100000 cap", total)
	}
}

func TestWorkerEvidence_UnderCapKeepsEverythingInShardOrder(t *testing.T) {
	workers := []hybridWorkerOutput{
		shardWorker(0, "first", 2, 100),
		{Index: 1, Shard: "failed", Err: fmt.Errorf("boom"), Evidence: shardWorker(1, "failed", 2, 100).Evidence},
		{Index: 2, Shard: "no-tools", Content: "prose only"},
		shardWorker(3, "last", 2, 100),
	}

	ev := workerEvidence(workers, 80_000)
	var labels []string
	for _, e := range ev {
		labels = append(labels, e.Label)
	}
	want := []string{"shard: first | item 0", "shard: first | item 1", "shard: last | item 0", "shard: last | item 1"}
	if strings.Join(labels, ",") != strings.Join(want, ",") {
		t.Errorf("ledger labels = %v, want %v (failed workers' evidence excluded)", labels, want)
	}
}

func TestWorkerEvidence_NonPositiveCapUsesDefault(t *testing.T) {
	workers := []hybridWorkerOutput{shardWorker(0, "only", 50, 5_000)}
	got := ledgerChars(workerEvidence(workers, 0))
	if got["only"] != defaultHybridEvidenceChars {
		t.Errorf("ledger = %d chars, want the %d default", got["only"], defaultHybridEvidenceChars)
	}
}

func TestFairShares(t *testing.T) {
	tests := []struct {
		name  string
		needs []int
		total int
		want  []int
	}{
		{"all over share", []int{100, 100, 100}, 90, []int{30, 30, 30}},
		{"all under cap", []int{10, 20, 30}, 100, []int{10, 20, 30}},
		{"leftover redistributed", []int{5, 100, 100}, 65, []int{5, 30, 30}},
		{"integer remainder stays within total", []int{100, 100, 100}, 100, []int{33, 33, 34}},
		{"zero total", []int{10, 10}, 0, []int{0, 0}},
		{"no needs", nil, 100, []int{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fairShares(tc.needs, tc.total)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("fairShares(%v, %d) = %v, want %v", tc.needs, tc.total, got, tc.want)
			}
			sum := 0
			for _, g := range got {
				sum += g
			}
			if sum > tc.total {
				t.Errorf("allocated %d, over the %d total", sum, tc.total)
			}
		})
	}
}

func TestHybridComplete_LedgerCapFromConfigCoversEveryShard(t *testing.T) {
	cfg := &config.Config{
		Claude: config.ClaudeConfig{Model: "opus"},
		Hybrid: config.HybridConfig{MaxEvidenceChars: 25_000},
	}
	aggregator := &stubProvider{name: "claude", resp: "final"}
	h := &Hybrid{
		cfg:               cfg,
		WorkerBackend:     "codex",
		WorkerModels:      []string{"w"},
		AggregatorBackend: "claude",
		AggregatorModel:   "opus",
		MaxParallel:       5,
		makeProvider: func(backend, model string) (Provider, error) {
			if backend == "claude" {
				return aggregator, nil
			}
			return &stubProvider{name: "codex", resp: "prose", evidence: []EvidenceRecord{
				{Label: "web_search: a", Content: strings.Repeat("a", 4_000)},
				{Label: "web_search: b", Content: strings.Repeat("b", 4_000)},
			}}, nil
		},
	}

	if _, err := h.Complete(context.Background(), Request{UserPrompt: "topic", Mode: "inquiry"}); err != nil {
		t.Fatal(err)
	}
	prompt := aggregator.lastReq.UserPrompt
	for _, shard := range branchSets["inquiry"] {
		if !strings.Contains(prompt, "| shard: "+shard+" |") {
			t.Errorf("aggregator ledger has no entry from shard %q", shard)
		}
	}

	var meta struct {
		Ledger struct {
			MaxChars       int `json:"max_chars"`
			ItemsCaptured  int `json:"items_captured"`
			ItemsPassed    int `json:"items_passed"`
			CharsCaptured  int `json:"chars_captured"`
			CharsPassed    int `json:"chars_passed"`
			ShardsCaptured int `json:"shards_captured"`
			ShardsPassed   int `json:"shards_passed"`
		} `json:"evidence_ledger"`
	}
	if err := json.Unmarshal([]byte(h.Metadata()), &meta); err != nil {
		t.Fatal(err)
	}
	l := meta.Ledger
	// 5 shards x 2 items x 4k = 40k captured; 25k cap = 5k per shard, so each
	// shard passes one whole item and one truncated one.
	if l.MaxChars != 25_000 || l.ItemsCaptured != 10 || l.CharsCaptured != 40_000 || l.ShardsCaptured != 5 {
		t.Errorf("ledger captured stats = %+v", l)
	}
	if l.ItemsPassed != 10 || l.CharsPassed != 25_000 || l.ShardsPassed != 5 {
		t.Errorf("ledger passed stats = %+v", l)
	}
}
