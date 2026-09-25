package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureDir lays out a small bake-off: arm A (bookworm style) on two
// topics, arm B (hybrid style) on one.
func fixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "out", "A-t1.md"), bookwormSample)
	writeFile(t, filepath.Join(dir, "out", "A-t2.md"), "## Findings\n\nOreskes documented the tobacco doubt campaign in exhaustive detail (2010).\n\n```json\nSOURCES_START\n[{\"title\":\"Merchants of Doubt\",\"author\":\"Naomi Oreskes\",\"url\":\"https://example.com/lifton\",\"date\":\"2010\"}]\nSOURCES_END\n```\n")
	writeFile(t, filepath.Join(dir, "out", "B-t1.md"), strings.Replace(hybridSample, "COINTELPRO ran from 1956 to 1971 [1].",
		"COINTELPRO ran from 1956 to 1971 under FBI direction across many field offices [1].", 1))
	writeFile(t, filepath.Join(dir, "out", "B-t1.json"), `{"metadata":{"workers":[{"model":"m"},{"model":"n","error":"boom"}]}}`)
	writeFile(t, filepath.Join(dir, "out", "notes.md"), "ignored: name does not match ARM-topic")
	writeFile(t, filepath.Join(dir, "logs", "calls.jsonl"),
		`{"arm":"A","topic":"t1","total_cost_usd":0.75,"web_searches":4}`+"\n"+
			`{"arm":"B","topic":"t1","total_cost_usd":1.5,"web_searches":0}`+"\n")
	writeFile(t, filepath.Join(dir, "logs", "times.tsv"), "A\tt1\t0\t242\t12:00:00\nB\tt1\t0\t600\t12:00:00\n")
	return dir
}

func TestMetricsCommand(t *testing.T) {
	dir := fixtureDir(t)
	var out bytes.Buffer
	if err := run([]string{"metrics", "-dir", dir}, &out); err != nil {
		t.Fatal(err)
	}
	table := out.String()
	for _, want := range []string{
		"| A-t1 |", "| A-t2 |", "| B-t1 |",
		"| 0.75 | 4 | 4m02s |",
		"Groundedness Review 2, Narrative vs. Evidence 0",
		"| A | 1 |", // one unique URL across A's topics (same URL twice)
	} {
		if !strings.Contains(table, want) {
			t.Errorf("metrics output missing %q:\n%s", want, table)
		}
	}
	if strings.Contains(table, "notes") {
		t.Error("non-matching files should be ignored")
	}

	data, err := os.ReadFile(filepath.Join(dir, "metrics.json"))
	if err != nil {
		t.Fatal(err)
	}
	var runs []RunMetrics
	if err := json.Unmarshal(data, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 || runs[2].FailedWorkers != 1 || runs[2].UniqueURLs != 4 {
		t.Errorf("metrics.json runs = %+v", runs)
	}
}

func TestSampleAndTally(t *testing.T) {
	dir := fixtureDir(t)
	var out bytes.Buffer
	if err := run([]string{"sample", "-dir", dir, "-n", "5", "-seed", "3"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "A: 2 claims sampled") || !strings.Contains(out.String(), "B: 1 claims sampled") {
		t.Errorf("sample summary:\n%s", out.String())
	}

	sheetPath := filepath.Join(dir, "review.md")
	sheet, err := os.ReadFile(sheetPath)
	if err != nil {
		t.Fatal(err)
	}
	filled := strings.Replace(string(sheet), "\nVerdict:\n", "\nVerdict: supported\n", -1)
	writeFile(t, sheetPath, filled)

	out.Reset()
	if err := run([]string{"tally", "-dir", dir}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| A | 2 | 2 | 2 | 0 | 0 | 0 | 100% |", "| B | 1 | 1 | 1 | 0 | 0 | 0 | 100% |"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("tally missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunErrors(t *testing.T) {
	var out bytes.Buffer
	if err := run(nil, &out); err == nil {
		t.Error("no args should error")
	}
	if err := run([]string{"bogus"}, &out); err == nil {
		t.Error("unknown command should error")
	}
	if err := run([]string{"metrics", "-dir", t.TempDir()}, &out); err == nil {
		t.Error("missing out/ should error")
	}
	if err := run([]string{"tally", "-dir", t.TempDir()}, &out); err == nil {
		t.Error("missing review sheet should error")
	}
}

func TestTallyCountsUnreviewed(t *testing.T) {
	key := []Claim{{ID: "C01", Arm: "A"}, {ID: "C02", Arm: "A"}, {ID: "C03", Arm: "B"}}
	got := Tally(key, map[string]string{"C01": "partial", "C03": "unverifiable"})
	if a := got["A"]; a.Sampled != 2 || a.Reviewed != 1 || a.Partial != 1 || a.SupportedRate() != 0 {
		t.Errorf("A = %+v", a)
	}
	if (ArmTally{}).SupportedRate() != 0 {
		t.Error("empty tally rate should be 0")
	}
}
