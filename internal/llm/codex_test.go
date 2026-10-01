package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/tools"
)

const codexSearchEvent = `{"type":"item.completed","item":{"id":"ws1","type":"web_search","query":"illusion of consensus DOI","action":{"type":"search","query":"illusion of consensus DOI"},"results":[{"type":"text_result","title":"The Illusion of Consensus","url":"https://doi.org/10.1177/0956797619856844","snippet":"Yousif, Aboody, Keil"}]}}`

// fakeCodex writes a script standing in for the codex binary. It records its
// argv and stdin under dir, prints events as the JSONL stream, writes
// lastMessage to the --output-last-message path, and exits with exitCode.
func fakeCodex(t *testing.T, events []string, lastMessage string, exitCode int) (binary, dir string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(strings.Join(events, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "last.txt"), []byte(lastMessage), 0644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
dir="` + dir + `"
printf '%s\n' "$@" > "$dir/args.txt"
pwd > "$dir/cwd.txt"
cat > "$dir/stdin.txt"
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    --output-last-message) shift; out="$1" ;;
  esac
  shift
done
cat "$dir/events.jsonl"
if [ -n "$out" ]; then cat "$dir/last.txt" > "$out"; fi
echo "progress noise on stderr" >&2
exit ` + string(rune('0'+exitCode)) + `
`
	binary = filepath.Join(dir, "fake-codex")
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return binary, dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

func TestCodex_Name(t *testing.T) {
	if got := (&Codex{}).Name(); got != "codex" {
		t.Errorf("Name() = %q, want codex", got)
	}
}

func TestCodex_Complete_CapturesFinalMessageEvidenceAndArgs(t *testing.T) {
	binary, dir := fakeCodex(t, []string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"item.completed","item":{"id":"m0","type":"agent_message","text":"searching first"}}`,
		codexSearchEvent,
		`{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"streamed answer"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":20}}`,
	}, "final answer from file\n", 0)

	c := &Codex{Binary: binary, Model: "gpt-test", ReasoningEffort: "high", IgnoreUserConfig: true}
	got, err := c.Complete(context.Background(), Request{
		SystemPrompt: "Be rigorous.",
		UserPrompt:   "What is the DOI?",
		Tools:        []tools.Tool{{Name: "web_search"}, {Name: "web_fetch"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got != "final answer from file" {
		t.Errorf("Complete() = %q, want the --output-last-message contents", got)
	}

	args := strings.Split(strings.TrimSpace(readFile(t, filepath.Join(dir, "args.txt"))), "\n")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"exec", "--json", "--ephemeral", "--skip-git-repo-check",
		"--sandbox read-only", "--ignore-user-config", "--model gpt-test",
		`model_reasoning_effort="high"`, `web_search="live"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
	if args[len(args)-1] != "-" {
		t.Errorf("last arg = %q, want - (prompt on stdin)", args[len(args)-1])
	}

	stdin := readFile(t, filepath.Join(dir, "stdin.txt"))
	if !strings.Contains(stdin, "<instructions>\nBe rigorous.\n</instructions>") || !strings.HasSuffix(stdin, "What is the DOI?") {
		t.Errorf("stdin prompt = %q, want system prompt folded ahead of the user prompt", stdin)
	}
	cwd := strings.TrimSpace(readFile(t, filepath.Join(dir, "cwd.txt")))
	if !strings.Contains(filepath.Base(cwd), "researchguy-codex-") {
		t.Errorf("codex ran in %q, want an isolated scratch dir", cwd)
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Errorf("scratch dir %q should be removed after the run", cwd)
	}

	ev := c.Evidence()
	if len(ev) != 1 {
		t.Fatalf("Evidence() = %d records, want 1", len(ev))
	}
	if ev[0].Label != "web_search: illusion of consensus DOI" {
		t.Errorf("evidence label = %q", ev[0].Label)
	}
	if !strings.Contains(ev[0].Content, "https://doi.org/10.1177/0956797619856844") || !strings.Contains(ev[0].Content, "Yousif, Aboody, Keil") {
		t.Errorf("evidence content = %q, want URL and snippet", ev[0].Content)
	}

	var meta struct {
		Backend         string          `json:"backend"`
		Model           string          `json:"model"`
		ReasoningEffort string          `json:"reasoning_effort"`
		WebSearches     int             `json:"web_searches"`
		EvidenceItems   int             `json:"evidence_items"`
		Usage           json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(c.Metadata()), &meta); err != nil {
		t.Fatalf("Metadata() not JSON: %v (%q)", err, c.Metadata())
	}
	if meta.Backend != "codex" || meta.Model != "gpt-test" || meta.ReasoningEffort != "high" || meta.WebSearches != 1 || meta.EvidenceItems != 1 {
		t.Errorf("metadata = %+v", meta)
	}
	if !strings.Contains(string(meta.Usage), `"input_tokens":100`) {
		t.Errorf("usage = %s, want turn.completed usage", meta.Usage)
	}
}

func TestCodex_Complete_NoToolsDisablesWebSearchAndOmitsUnsetFlags(t *testing.T) {
	binary, dir := fakeCodex(t, []string{`{"type":"turn.completed","usage":{}}`}, "ok", 0)

	c := &Codex{Binary: binary}
	if _, err := c.Complete(context.Background(), Request{UserPrompt: "hi"}); err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	joined := strings.ReplaceAll(readFile(t, filepath.Join(dir, "args.txt")), "\n", " ")
	if !strings.Contains(joined, `web_search="disabled"`) {
		t.Errorf("args %q should disable web search without tools", joined)
	}
	for _, unwanted := range []string{"--model", "model_reasoning_effort", "--ignore-user-config"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("args %q should not contain %q when unset", joined, unwanted)
		}
	}
	if stdin := readFile(t, filepath.Join(dir, "stdin.txt")); stdin != "hi" {
		t.Errorf("stdin = %q, want the bare user prompt when there is no system prompt", stdin)
	}
}

func TestCodex_Complete_FallsBackToLastAgentMessage(t *testing.T) {
	binary, _ := fakeCodex(t, []string{
		`{"type":"item.completed","item":{"type":"agent_message","text":"first"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"  last message  "}}`,
	}, "", 0)

	got, err := (&Codex{Binary: binary}).Complete(context.Background(), Request{UserPrompt: "hi"})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got != "last message" {
		t.Errorf("Complete() = %q, want the last agent_message", got)
	}
}

func TestCodex_Complete_TurnFailedReturnsCodexError(t *testing.T) {
	binary, _ := fakeCodex(t, []string{
		`{"type":"error","message":"stream error"}`,
		`{"type":"turn.failed","error":{"message":"model is not supported"}}`,
	}, "", 1)

	c := &Codex{Binary: binary, Model: "bad"}
	_, err := c.Complete(context.Background(), Request{UserPrompt: "hi"})
	if err == nil || !strings.Contains(err.Error(), "model is not supported") {
		t.Fatalf("err = %v, want the turn.failed message", err)
	}
	if !strings.Contains(c.Metadata(), "model is not supported") {
		t.Errorf("metadata %q should record the failure", c.Metadata())
	}
}

func TestCodex_Complete_NonZeroExitWithoutEventsIncludesStderr(t *testing.T) {
	binary, _ := fakeCodex(t, nil, "", 2)

	_, err := (&Codex{Binary: binary}).Complete(context.Background(), Request{UserPrompt: "hi"})
	if err == nil || !strings.Contains(err.Error(), "progress noise on stderr") {
		t.Fatalf("err = %v, want CLI failure with stderr", err)
	}
}

func TestCodex_Complete_NoFinalMessage(t *testing.T) {
	binary, _ := fakeCodex(t, []string{`{"type":"turn.completed","usage":{}}`}, "  \n", 0)

	_, err := (&Codex{Binary: binary}).Complete(context.Background(), Request{UserPrompt: "hi"})
	if err == nil || !strings.Contains(err.Error(), "no final message") {
		t.Fatalf("err = %v, want no final message error", err)
	}
}

func TestCodex_Complete_BinaryNotFound(t *testing.T) {
	_, err := (&Codex{Binary: "/nonexistent/codex"}).Complete(context.Background(), Request{UserPrompt: "hi"})
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestCodex_Complete_ResetsEvidenceBetweenRuns(t *testing.T) {
	binary, dir := fakeCodex(t, []string{codexSearchEvent}, "one", 0)
	c := &Codex{Binary: binary}
	if _, err := c.Complete(context.Background(), Request{UserPrompt: "a"}); err != nil {
		t.Fatal(err)
	}
	if len(c.Evidence()) != 1 {
		t.Fatalf("first run evidence = %d, want 1", len(c.Evidence()))
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(`{"type":"turn.completed"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Complete(context.Background(), Request{UserPrompt: "b"}); err != nil {
		t.Fatal(err)
	}
	if len(c.Evidence()) != 0 {
		t.Errorf("second run evidence = %d, want 0 (stale evidence leaked)", len(c.Evidence()))
	}
}

func TestParseCodexEvents_SkipsNoiseAndEmptySearches(t *testing.T) {
	stream := strings.Join([]string{
		"Reading additional input from stdin...",
		"",
		"{not json",
		`{"type":"item.completed"}`,
		`{"type":"item.completed","item":{"type":"web_search","query":"q","action":{"type":"search"},"results":[]}}`,
		`{"type":"item.completed","item":{"type":"web_search","action":{"type":"open_page","url":"https://example.org/a"},"results":[{"title":"A","url":"https://example.org/a","snippet":"page text"}]}}`,
		`{"type":"item.completed","item":{"type":"command_execution","text":"ignored"}}`,
	}, "\n")

	run := parseCodexEvents([]byte(stream))
	if run.webSearches != 2 {
		t.Errorf("webSearches = %d, want 2", run.webSearches)
	}
	if len(run.evidence) != 1 {
		t.Fatalf("evidence = %d, want 1 (empty result sets carry no evidence)", len(run.evidence))
	}
	if run.evidence[0].Label != "web_search open_page: https://example.org/a" {
		t.Errorf("label = %q", run.evidence[0].Label)
	}
	if run.failure != "" || run.lastMessage != "" {
		t.Errorf("run = %+v, want no failure or message", run)
	}
}

func TestParseCodexEvents_ErrorWithoutTurnFailed(t *testing.T) {
	run := parseCodexEvents([]byte(`{"type":"error","message":"reconnecting failed"}`))
	if run.failure != "reconnecting failed" {
		t.Errorf("failure = %q", run.failure)
	}
}

func TestNewProvider_Codex(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Codex = config.CodexConfig{Binary: "/opt/codex", Model: "gpt-cfg", ReasoningEffort: "medium", IgnoreUserConfig: true}

	p, err := NewProvider(cfg, "codex", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c, ok := p.(*Codex)
	if !ok {
		t.Fatalf("expected *Codex, got %T", p)
	}
	if c.Binary != "/opt/codex" || c.Model != "gpt-cfg" || c.ReasoningEffort != "medium" || !c.IgnoreUserConfig {
		t.Errorf("codex provider = %+v", c)
	}

	p, err = NewProvider(cfg, "codex", "gpt-override")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(*Codex).Model; got != "gpt-override" {
		t.Errorf("Model = %q, want the override", got)
	}
}

func TestHybridResolveWorkerModels_FallsBackToWorkerBackendModel(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Codex.Model = "gpt-cfg"
	h := &Hybrid{cfg: cfg}

	if got := h.resolveWorkerModels("codex"); len(got) != 1 || got[0] != "gpt-cfg" {
		t.Errorf("codex fallback = %v, want [gpt-cfg]", got)
	}
	if got := h.resolveWorkerModels("ollama"); len(got) != 1 || got[0] != "glm-4.7-flash" {
		t.Errorf("ollama fallback = %v, want [glm-4.7-flash]", got)
	}
	cfg.Codex.Model = ""
	if got := h.resolveWorkerModels("codex"); len(got) != 0 {
		t.Errorf("codex with no model anywhere = %v, want none (never an Ollama model)", got)
	}
}

// TestHybrid_CodexWorkersClaudeAggregator runs the real provider wiring with
// fake codex and claude binaries: every shard goes to codex, and the claude
// aggregator receives the codex search results in its evidence ledger.
func TestHybrid_CodexWorkersClaudeAggregator(t *testing.T) {
	codexBin, codexDir := fakeCodex(t, []string{codexSearchEvent}, "worker finding", 0)

	claudeDir := t.TempDir()
	claudeBin := filepath.Join(claudeDir, "fake-claude")
	// Echo the -p prompt so the test can inspect what the aggregator saw.
	script := "#!/bin/sh\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = \"-p\" ]; then shift; printf '%s' \"$1\" > \"" + claudeDir + "/prompt.txt\"; fi\n  shift\ndone\necho 'aggregated report'\n"
	if err := os.WriteFile(claudeBin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	cfg := defaultTestConfig()
	cfg.DefaultBackend = "hybrid"
	cfg.Codex = config.CodexConfig{Binary: codexBin, ReasoningEffort: "high", IgnoreUserConfig: true}
	cfg.Claude.Binary = claudeBin
	cfg.Hybrid = config.HybridConfig{
		WorkerBackend:     "codex",
		WorkerModels:      []string{"gpt-worker"},
		AggregatorBackend: "claude",
		AggregatorModel:   "opus",
		MaxParallel:       5,
	}

	p, err := NewProvider(cfg, "", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Complete(context.Background(), Request{UserPrompt: "topic", Mode: "inquiry", Tools: []tools.Tool{{Name: "web_search"}}})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got != "aggregated report" {
		t.Errorf("Complete() = %q, want the aggregator output", got)
	}

	prompt := readFile(t, filepath.Join(claudeDir, "prompt.txt"))
	if n := strings.Count(prompt, "(codex/gpt-worker) ---"); n != len(branchSets["inquiry"]) {
		t.Errorf("aggregator saw %d codex worker outputs, want %d", n, len(branchSets["inquiry"]))
	}
	if !strings.Contains(prompt, "| codex/gpt-worker | shard: ") || !strings.Contains(prompt, "https://doi.org/10.1177/0956797619856844") {
		t.Errorf("aggregator prompt missing codex evidence ledger entries:\n%s", prompt)
	}
	if args := readFile(t, filepath.Join(codexDir, "args.txt")); !strings.Contains(args, "gpt-worker") {
		t.Errorf("codex args %q should use the hybrid worker model", args)
	}

	var meta struct {
		AggregatorBackend string `json:"aggregator_backend"`
		Workers           []struct {
			Backend       string `json:"backend"`
			EvidenceItems int    `json:"evidence_items"`
		} `json:"workers"`
	}
	if err := json.Unmarshal([]byte(p.(*Hybrid).Metadata()), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.AggregatorBackend != "claude" || len(meta.Workers) != len(branchSets["inquiry"]) {
		t.Fatalf("metadata = %+v", meta)
	}
	for _, w := range meta.Workers {
		if w.Backend != "codex" || w.EvidenceItems != 1 {
			t.Errorf("worker metadata = %+v, want codex with 1 evidence item", w)
		}
	}
}
