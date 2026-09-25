package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCallsAndSum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calls.jsonl")
	writeFile(t, path, `{"arm":"A","topic":"t1","model":"sonnet","total_cost_usd":0.5,"web_searches":4,"exit":0}

{"arm":"A","topic":"t1","model":"sonnet","total_cost_usd":0.25,"web_searches":1,"exit":0,"is_error":true}
{"arm":"B","topic":"t1","model":"opus","total_cost_usd":1.0,"exit":1}
`)
	calls, err := LoadCalls(path)
	if err != nil || len(calls) != 3 {
		t.Fatalf("LoadCalls: %d, %v", len(calls), err)
	}
	sums := SumCalls(calls)
	a := sums["A-t1"]
	if a.Calls != 2 || a.CostUSD != 0.75 || a.WebSearches != 5 || a.Errors != 1 {
		t.Errorf("A-t1 = %+v", a)
	}
	if sums["B-t1"].Errors != 1 {
		t.Errorf("non-zero exit counts as error: %+v", sums["B-t1"])
	}
}

func TestLoadCallsBadLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calls.jsonl")
	writeFile(t, path, "{\"arm\":\"A\"}\nnot json\n")
	if _, err := LoadCalls(path); err == nil || !strings.Contains(err.Error(), ":2:") {
		t.Fatalf("want line-numbered error, got %v", err)
	}
}

func TestLoadTimes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "times.tsv")
	writeFile(t, path, "A\tt1\t1\t100\t12:00:00\nB\tt1\t0\t300\t12:00:00\nA\tt1\t0\t242\t12:30:00\n")
	times, err := LoadTimes(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := times["A-t1"]; got.Seconds != 242 || got.Exit != 0 {
		t.Errorf("rerun should replace the earlier row: %+v", got)
	}
	if len(times) != 2 {
		t.Errorf("want 2 runs, got %d", len(times))
	}

	writeFile(t, path, "A\tt1\tx\t10\n")
	if _, err := LoadTimes(path); err == nil {
		t.Error("bad exit code should error")
	}
	writeFile(t, path, "A\tt1\n")
	if _, err := LoadTimes(path); err == nil {
		t.Error("short row should error")
	}
}

func TestLoadHybridMeta(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "B-t1.json")
	writeFile(t, path, `{"report":"x","backend":"hybrid","metadata":{"verified":true,"duration_ms":5,"workers":[{"model":"m1","shard":"s1"},{"model":"m2","shard":"s2","error":"timeout"}]}}`)
	m, err := LoadHybridMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Verified || len(m.Workers) != 2 || m.FailedWorkers() != 1 {
		t.Errorf("meta = %+v", m)
	}

	writeFile(t, path, `{"report":"x"}`)
	if _, err := LoadHybridMeta(path); err == nil {
		t.Error("missing metadata should error")
	}
	writeFile(t, path, ``)
	if _, err := LoadHybridMeta(path); err == nil {
		t.Error("empty file should error")
	}
}
