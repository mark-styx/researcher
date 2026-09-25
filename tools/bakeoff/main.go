// Command bakeoff measures research outputs from the Phase 4 research
// quality bake-off: sources, URL resolution, counter-evidence, critic flags,
// cost, and time per arm, plus a blinded claim sample for hand review.
//
// Layout of -dir:
//
//	out/<ARM>-<topic>.md    research output per arm x topic
//	out/<ARM>-<topic>.json  `researchguy dive --json` output (hybrid arms)
//	logs/calls.jsonl        per-call cost log from the claude wrapper
//	logs/times.tsv          arm, topic, exit, seconds, start
//
// Usage:
//
//	go run ./tools/bakeoff metrics -dir D [-check]
//	go run ./tools/bakeoff sample  -dir D [-n 20] [-seed 1]
//	go run ./tools/bakeoff tally   -dir D
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "bakeoff:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: bakeoff metrics|sample|tally -dir D")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	dir := fs.String("dir", ".", "bake-off directory")
	check := fs.Bool("check", false, "metrics: fetch every URL (cached in urlcheck.json)")
	n := fs.Int("n", 20, "sample: claims per arm")
	seed := fs.Uint64("seed", 1, "sample: random seed")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "metrics":
		return cmdMetrics(*dir, *check, stdout)
	case "sample":
		return cmdSample(*dir, *n, *seed, stdout)
	case "tally":
		return cmdTally(*dir, stdout)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// Output is one arm x topic research output.
type Output struct {
	Arm, Topic string
	Raw        string
	Doc        Doc
}

var outNameRe = regexp.MustCompile(`^([A-Z])-(t\d+)\.md$`)

// LoadOutputs reads out/<ARM>-<topic>.md files, sorted by arm then topic.
func LoadOutputs(dir string) ([]Output, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "out"))
	if err != nil {
		return nil, err
	}
	var out []Output
	for _, e := range entries {
		m := outNameRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, "out", e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, Output{Arm: m[1], Topic: m[2], Raw: string(data), Doc: ParseDoc(string(data))})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Arm != out[j].Arm {
			return out[i].Arm < out[j].Arm
		}
		return out[i].Topic < out[j].Topic
	})
	return out, nil
}

// RunMetrics is everything measured for one arm x topic run.
type RunMetrics struct {
	Arm, Topic      string
	Words           int
	UniqueURLs      int
	URLs            []string // first raw URL per normalized identity
	ListedSources   int      // bookworm sources block or references list entries
	Claims          int      // attributed sentences
	CounterItems    int
	CounterHeadings []string
	CounterSents    int
	CounterExamples []string
	CriticFlags     map[string]int
	FailedWorkers   int
	CostUSD         float64
	Calls           int
	CallErrors      int
	WebSearches     int
	Seconds         int
	Exit            int
	Checks          map[string]int
}

func measure(o Output) RunMetrics {
	uniq := UniqueURLs(ExtractURLs(o.Raw))
	urls := make([]string, 0, len(uniq))
	for _, k := range sortedKeys(uniq) {
		urls = append(urls, uniq[k])
	}
	counter, heads := CounterEvidence(o.Doc.Sections)
	csents, cex := CounterSentences(o.Doc.Body)
	listed := len(o.Doc.Sources)
	if listed == 0 {
		listed = len(o.Doc.Refs)
	}
	return RunMetrics{
		Arm: o.Arm, Topic: o.Topic,
		Words:           len(strings.Fields(o.Doc.Body)),
		UniqueURLs:      len(uniq),
		URLs:            urls,
		ListedSources:   listed,
		Claims:          len(ExtractClaims(o.Arm, o.Topic, o.Doc)),
		CounterItems:    counter,
		CounterHeadings: heads,
		CounterSents:    csents,
		CounterExamples: cex,
		CriticFlags:     CriticFlags(o.Doc.Critic),
	}
}

func cmdMetrics(dir string, check bool, w io.Writer) error {
	outputs, err := LoadOutputs(dir)
	if err != nil {
		return err
	}
	calls, err := LoadCalls(filepath.Join(dir, "logs", "calls.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	callTotals := SumCalls(calls)
	times, err := LoadTimes(filepath.Join(dir, "logs", "times.tsv"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var runs []RunMetrics
	for _, o := range outputs {
		m := measure(o)
		k := RunKey(o.Arm, o.Topic)
		if t, ok := callTotals[k]; ok {
			m.CostUSD, m.Calls, m.CallErrors, m.WebSearches = t.CostUSD, t.Calls, t.Errors, t.WebSearches
		}
		if t, ok := times[k]; ok {
			m.Seconds, m.Exit = t.Seconds, t.Exit
		}
		if meta, err := LoadHybridMeta(filepath.Join(dir, "out", k+".json")); err == nil {
			m.FailedWorkers = meta.FailedWorkers()
		}
		runs = append(runs, m)
	}

	var results map[string]CheckResult
	if check {
		cachePath := filepath.Join(dir, "urlcheck.json")
		cache, err := LoadCache(cachePath)
		if err != nil {
			return err
		}
		var all []string
		for _, r := range runs {
			all = append(all, r.URLs...)
		}
		fmt.Fprintf(os.Stderr, "checking %d URLs (%d cached)...\n", len(all), len(cache))
		results = CheckAll(context.Background(), NewCheckClient(20*time.Second), all, cache, 8)
		if err := SaveCache(cachePath, results); err != nil {
			return err
		}
		for i := range runs {
			runs[i].Checks = ClassCounts(runs[i].URLs, results)
		}
	}

	if err := writeMetrics(w, runs, results != nil); err != nil {
		return err
	}
	data, err := json.MarshalIndent(runs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "metrics.json"), append(data, '\n'), 0o644)
}

func writeMetrics(w io.Writer, runs []RunMetrics, checked bool) error {
	fmt.Fprintln(w, "| Run | Words | Unique URLs | Listed sources | Attributed claims | Counter-evidence items (headed) | Limiting sentences | Critic flags | Failed workers | Claude calls | Cost USD | Web searches | Wall time |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, r := range runs {
		fmt.Fprintf(w, "| %s | %d | %d | %d | %d | %d | %d | %s | %d | %d | %.2f | %d | %s |\n",
			RunKey(r.Arm, r.Topic), r.Words, r.UniqueURLs, r.ListedSources, r.Claims,
			r.CounterItems, r.CounterSents, formatFlags(r.CriticFlags), r.FailedWorkers, r.Calls, r.CostUSD,
			r.WebSearches, formatDuration(r.Seconds))
	}

	// Per-arm totals: URLs are unioned across topics, the rest summed.
	type armTotal struct {
		urls                      map[string]string
		words, claims, counter    int
		counterSents              int
		flags, seconds, calls, ws int
		cost                      float64
		checks                    map[string]int
	}
	arms := make(map[string]*armTotal)
	for _, r := range runs {
		a := arms[r.Arm]
		if a == nil {
			a = &armTotal{urls: make(map[string]string), checks: make(map[string]int)}
			arms[r.Arm] = a
		}
		for k, v := range UniqueURLs(r.URLs) {
			a.urls[k] = v
		}
		a.words += r.Words
		a.claims += r.Claims
		a.counter += r.CounterItems
		a.counterSents += r.CounterSents
		for _, n := range r.CriticFlags {
			a.flags += n
		}
		a.seconds += r.Seconds
		a.calls += r.Calls
		a.ws += r.WebSearches
		a.cost += r.CostUSD
		for k, v := range r.Checks {
			a.checks[k] += v
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Arm | Unique URLs (all topics) | Attributed claims | Counter-evidence items (headed) | Limiting sentences | Critic flags | Cost USD | Wall time (sum) |"+checkHeader(checked))
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|"+checkRule(checked))
	for _, name := range sortedKeys(arms) {
		a := arms[name]
		fmt.Fprintf(w, "| %s | %d | %d | %d | %d | %d | %.2f | %s |%s\n", name, len(a.urls), a.claims,
			a.counter, a.counterSents, a.flags, a.cost, formatDuration(a.seconds), checkCells(checked, a.checks))
	}
	return nil
}

func checkHeader(checked bool) string {
	if !checked {
		return ""
	}
	return " URLs ok | Bot-blocked | Dead | Other errors | Resolution rate |"
}

func checkRule(checked bool) string {
	if !checked {
		return ""
	}
	return "---|---|---|---|---|"
}

// checkCells formats per-URL-occurrence check counts. Per-arm counts sum the
// per-topic runs, so a URL cited under two topics counts twice here.
func checkCells(checked bool, c map[string]int) string {
	if !checked {
		return ""
	}
	total := c[ClassOK] + c[ClassBlocked] + c[ClassDead] + c[ClassError]
	rate := 0.0
	if total > 0 {
		rate = float64(c[ClassOK]) / float64(total) * 100
	}
	return fmt.Sprintf(" %d | %d | %d | %d | %.0f%% |", c[ClassOK], c[ClassBlocked], c[ClassDead], c[ClassError], rate)
}

func formatFlags(m map[string]int) string {
	if len(m) == 0 {
		return "-"
	}
	var parts []string
	for _, k := range sortedKeys(m) {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

func formatDuration(secs int) string {
	if secs == 0 {
		return "-"
	}
	return fmt.Sprintf("%dm%02ds", secs/60, secs%60)
}

func cmdSample(dir string, n int, seed uint64, w io.Writer) error {
	outputs, err := LoadOutputs(dir)
	if err != nil {
		return err
	}
	var all []Claim
	for _, o := range outputs {
		all = append(all, ExtractClaims(o.Arm, o.Topic, o.Doc)...)
	}
	blinded := Blind(SampleClaims(all, n, seed), seed)

	sheet, err := os.Create(filepath.Join(dir, "review.md"))
	if err != nil {
		return err
	}
	if err := WriteReviewSheet(sheet, blinded); err != nil {
		sheet.Close()
		return err
	}
	if err := sheet.Close(); err != nil {
		return err
	}
	key, err := json.MarshalIndent(blinded, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "review-key.json"), append(key, '\n'), 0o644); err != nil {
		return err
	}
	perArm := make(map[string]int)
	for _, c := range blinded {
		perArm[c.Arm]++
	}
	for _, a := range sortedKeys(perArm) {
		fmt.Fprintf(w, "%s: %d claims sampled\n", a, perArm[a])
	}
	fmt.Fprintf(w, "wrote %s and %s\n", filepath.Join(dir, "review.md"), filepath.Join(dir, "review-key.json"))
	return nil
}

func cmdTally(dir string, w io.Writer) error {
	f, err := os.Open(filepath.Join(dir, "review.md"))
	if err != nil {
		return err
	}
	defer f.Close()
	verdicts, err := Verdicts(f)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, "review-key.json"))
	if err != nil {
		return err
	}
	var key []Claim
	if err := json.Unmarshal(data, &key); err != nil {
		return err
	}
	return writeTally(w, Tally(key, verdicts))
}

// ArmTally counts verdicts for one arm.
type ArmTally struct {
	Sampled, Reviewed                             int
	Supported, Partial, Unsupported, Unverifiable int
}

// SupportedRate is supported claims over reviewed claims.
func (t ArmTally) SupportedRate() float64 {
	if t.Reviewed == 0 {
		return 0
	}
	return float64(t.Supported) / float64(t.Reviewed)
}

// Tally joins verdicts to the blinding key.
func Tally(key []Claim, verdicts map[string]string) map[string]ArmTally {
	out := make(map[string]ArmTally)
	for _, c := range key {
		t := out[c.Arm]
		t.Sampled++
		switch verdicts[c.ID] {
		case "supported":
			t.Supported++
		case "partial":
			t.Partial++
		case "unsupported":
			t.Unsupported++
		case "unverifiable":
			t.Unverifiable++
		}
		if _, ok := verdicts[c.ID]; ok {
			t.Reviewed++
		}
		out[c.Arm] = t
	}
	return out
}

func writeTally(w io.Writer, t map[string]ArmTally) error {
	fmt.Fprintln(w, "| Arm | Sampled | Reviewed | Supported | Partial | Unsupported | Unverifiable | Supported rate |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|")
	for _, a := range sortedKeys(t) {
		x := t[a]
		fmt.Fprintf(w, "| %s | %d | %d | %d | %d | %d | %d | %.0f%% |\n", a, x.Sampled, x.Reviewed,
			x.Supported, x.Partial, x.Unsupported, x.Unverifiable, x.SupportedRate()*100)
	}
	return nil
}
