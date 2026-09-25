package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// CallRecord is one claude CLI call logged by the bake-off wrapper.
type CallRecord struct {
	Arm         string   `json:"arm"`
	Topic       string   `json:"topic"`
	Model       string   `json:"model"`
	Tools       []string `json:"tools"`
	WallS       float64  `json:"wall_s"`
	Exit        int      `json:"exit"`
	CostUSD     float64  `json:"total_cost_usd"`
	NumTurns    int      `json:"num_turns"`
	IsError     bool     `json:"is_error"`
	Subtype     string   `json:"subtype"`
	WebSearches int      `json:"web_searches"`
}

// RunTime is one timed arm x topic run from times.tsv.
type RunTime struct {
	Arm     string
	Topic   string
	Exit    int
	Seconds int
}

// LoadCalls reads the wrapper's JSONL call log.
func LoadCalls(path string) ([]CallRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []CallRecord
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r CallRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// LoadTimes reads times.tsv: arm, topic, exit code, seconds, start clock.
// A rerun of the same arm x topic replaces the earlier row.
func LoadTimes(path string) (map[string]RunTime, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]RunTime)
	for n, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 4 {
			return nil, fmt.Errorf("%s:%d: want 4+ tab-separated fields, got %d", path, n+1, len(f))
		}
		exit, err1 := strconv.Atoi(f[2])
		secs, err2 := strconv.Atoi(f[3])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("%s:%d: bad exit code or seconds", path, n+1)
		}
		out[RunKey(f[0], f[1])] = RunTime{Arm: f[0], Topic: f[1], Exit: exit, Seconds: secs}
	}
	return out, nil
}

// RunKey identifies an arm x topic run.
func RunKey(arm, topic string) string { return arm + "-" + topic }

// CallTotals sums cost, calls, and web searches per arm x topic.
type CallTotals struct {
	CostUSD     float64
	Calls       int
	Errors      int
	WebSearches int
}

func SumCalls(calls []CallRecord) map[string]CallTotals {
	out := make(map[string]CallTotals)
	for _, c := range calls {
		k := RunKey(c.Arm, c.Topic)
		t := out[k]
		t.CostUSD += c.CostUSD
		t.Calls++
		t.WebSearches += c.WebSearches
		if c.IsError || c.Exit != 0 {
			t.Errors++
		}
		out[k] = t
	}
	return out
}

// HybridMeta is the part of a hybrid dive's --json metadata the report uses.
type HybridMeta struct {
	Workers []struct {
		Model string `json:"model"`
		Shard string `json:"shard"`
		Error string `json:"error"`
	} `json:"workers"`
	Verified   bool  `json:"verified"`
	DurationMS int64 `json:"duration_ms"`
}

// LoadHybridMeta reads the metadata object from a `dive --json` output.
func LoadHybridMeta(path string) (*HybridMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var wrapper struct {
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(wrapper.Metadata) == 0 {
		return nil, fmt.Errorf("%s: no metadata", path)
	}
	var m HybridMeta
	if err := json.Unmarshal(wrapper.Metadata, &m); err != nil {
		return nil, fmt.Errorf("%s: metadata: %w", path, err)
	}
	return &m, nil
}

// FailedWorkers counts workers that returned an error.
func (m *HybridMeta) FailedWorkers() int {
	n := 0
	for _, w := range m.Workers {
		if w.Error != "" {
			n++
		}
	}
	return n
}
