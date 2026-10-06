package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/tools"
)

// Events as goose 1.50 prints them with --output-format stream-json: text
// and thinking stream as deltas that share a message id, and a stdio
// extension's tools are prefixed with the extension's name.
const (
	gooseThinking   = `{"type":"message","message":{"id":"m1","role":"assistant","content":[{"type":"thinking","thinking":"Let me search","signature":""}],"metadata":{"inference":{"provider":"openai","requestedModel":"glm-test"}}}}`
	gooseTextBefore = `{"type":"message","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"I'll search first."}],"metadata":{"inference":{"provider":"openai","requestedModel":"glm-test"}}}}`
	gooseSearchReq  = `{"type":"message","message":{"id":"m1","role":"assistant","content":[{"type":"toolRequest","id":"call_s1","toolCall":{"status":"success","value":{"name":"researchguy__web_search","arguments":{"query":"fda not a horse"}}},"_meta":{"goose_extension":"researchguy"}}]}}`
	gooseSearchResp = `{"type":"message","message":{"id":"u1","role":"user","content":[{"type":"toolResponse","id":"call_s1","toolResult":{"status":"success","value":{"content":[{"type":"text","text":"1. **FDA post**\n   URL: https://x.com/US_FDA/status/1429050070243192839\n   You are not a horse.\n"}],"structuredContent":{"results":[{"rank":1,"title":"FDA post","url":"https://x.com/US_FDA/status/1429050070243192839","snippet":"You are not a horse."},{"rank":2,"title":"Consumer update","url":"https://www.fda.gov/consumers/ivermectin","snippet":""}]}}}}]}}`
	gooseFetchReq   = `{"type":"message","message":{"id":"m2","role":"assistant","content":[{"type":"toolRequest","id":"call_f1","toolCall":{"status":"success","value":{"name":"researchguy__web_fetch","arguments":{"url":"https://www.fda.gov/consumers/ivermectin"}}}}]}}`
	gooseFetchResp  = `{"type":"message","message":{"id":"u2","role":"user","content":[{"type":"toolResponse","id":"call_f1","toolResult":{"status":"success","value":{"content":[{"type":"text","text":"Why You Should Not Use Ivermectin to Treat or Prevent COVID-19"}]}}}]}}`
	gooseFailedReq  = `{"type":"message","message":{"id":"m3","role":"assistant","content":[{"type":"toolRequest","id":"call_f2","toolCall":{"status":"success","value":{"name":"researchguy__web_fetch","arguments":{"url":"https://blocked.example/"}}}}]}}`
	gooseFailedResp = `{"type":"message","message":{"id":"u3","role":"user","content":[{"type":"toolResponse","id":"call_f2","toolResult":{"status":"success","value":{"content":[{"type":"text","text":"fetch failed: status 403"}],"isError":true}}}]}}`
	gooseComplete   = `{"type":"complete","total_tokens":3873,"input_tokens":3857,"output_tokens":16}`
)

// gooseAnswer streams text as deltas of one assistant message.
func gooseAnswer(id string, deltas ...string) []string {
	var out []string
	for _, d := range deltas {
		b, _ := json.Marshal(d)
		out = append(out, `{"type":"message","message":{"id":"`+id+`","role":"assistant","content":[{"type":"text","text":`+string(b)+`}]}}`)
	}
	return out
}

// fakeGoose writes a script standing in for the goose binary. It records its
// argv (one per line), working dir and stdin under dir, prints goose's
// banner and then events, and exits with exitCode.
func fakeGoose(t *testing.T, events []string, exitCode int) (binary, dir string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(strings.Join(events, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
dir="` + dir + `"
printf '%s\n' "$@" > "$dir/args.txt"
pwd > "$dir/cwd.txt"
cat > "$dir/stdin.txt"
echo '    __( O)>  new session'
echo '     L L     goose is ready'
cat "$dir/events.jsonl"
echo "goose noise on stderr" >&2
exit ` + fmt.Sprint(exitCode) + `
`
	binary = filepath.Join(dir, "fake-goose")
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return binary, dir
}

func gooseArgs(t *testing.T, dir string) []string {
	t.Helper()
	return strings.Split(strings.TrimSpace(readFile(t, filepath.Join(dir, "args.txt"))), "\n")
}

// argAfter returns the argument after flag, or "" if flag isn't there.
func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestGoose_Name(t *testing.T) {
	if got := (&Goose{}).Name(); got != "goose" {
		t.Errorf("Name() = %q, want goose", got)
	}
}

func TestGoose_Complete_EvidenceFinalMessageAndArgs(t *testing.T) {
	events := []string{gooseThinking, gooseTextBefore, gooseSearchReq, gooseSearchResp, gooseFetchReq, gooseFetchResp, gooseFailedReq, gooseFailedResp}
	events = append(events, gooseAnswer("m4", "The FDA ", "posted it on ", "August 21, 2021.")...)
	events = append(events, gooseComplete)
	binary, dir := fakeGoose(t, events, 0)

	var captured []EvidenceRecord
	g := &Goose{Binary: binary, Provider: "openai", Model: "glm-test", MaxTurns: 12, WebCommand: "/opt/rg mcp --profile web"}
	got, err := g.Complete(context.Background(), Request{
		SystemPrompt: "Be rigorous.",
		UserPrompt:   "When did the FDA post it?",
		Tools:        tools.DefaultTools(),
		Capture: func(rec EvidenceRecord) string {
			captured = append(captured, rec)
			return fmt.Sprintf("E%d", 100+len(captured))
		},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got != "The FDA posted it on August 21, 2021." {
		t.Errorf("Complete() = %q, want the last assistant message's deltas joined", got)
	}

	args := gooseArgs(t, dir)
	joined := strings.Join(args, " ")
	for _, want := range []string{"run", "--no-session", "--no-profile", "--output-format stream-json", "--max-tool-repetitions 3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
	for flag, want := range map[string]string{
		"--max-turns":      "12",
		"--provider":       "openai",
		"--model":          "glm-test",
		"--system":         "Be rigorous.",
		"--with-extension": "researchguy:/opt/rg mcp --profile web",
		"-i":               "-",
	} {
		if got := argAfter(args, flag); got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
	if stdin := readFile(t, filepath.Join(dir, "stdin.txt")); stdin != "When did the FDA post it?" {
		t.Errorf("stdin = %q, want the user prompt alone", stdin)
	}
	cwd := strings.TrimSpace(readFile(t, filepath.Join(dir, "cwd.txt")))
	if !strings.Contains(filepath.Base(cwd), "researchguy-goose-") {
		t.Errorf("goose ran in %q, want an isolated scratch dir", cwd)
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Errorf("scratch dir %q should be removed after the run", cwd)
	}

	ev := g.Evidence()
	if len(ev) != 2 || len(captured) != 2 {
		t.Fatalf("Evidence() = %d records, captured %d, want 2 each (the failed fetch isn't evidence)", len(ev), len(captured))
	}
	search, fetch := ev[0], ev[1]
	if search.ID != "E101" || fetch.ID != "E102" {
		t.Errorf("evidence IDs = %q, %q, want the capture's E101, E102", search.ID, fetch.ID)
	}
	if search.Label != `web_search {"query":"fda not a horse"}` {
		t.Errorf("search label = %q", search.Label)
	}
	wantSearch := store.Call{Tool: "web_search", Action: "search", Query: "fda not a horse", Results: []store.CaptureResult{
		{Rank: 1, Title: "FDA post", URL: "https://x.com/US_FDA/status/1429050070243192839", Snippet: "You are not a horse."},
		{Rank: 2, Title: "Consumer update", URL: "https://www.fda.gov/consumers/ivermectin"},
	}}
	if fmt.Sprint(search.Call) != fmt.Sprint(wantSearch) {
		t.Errorf("search call = %+v, want %+v", search.Call, wantSearch)
	}
	if !strings.Contains(search.Content, "You are not a horse.") {
		t.Errorf("search content = %q", search.Content)
	}
	if fetch.Call.Action != "fetch" || fetch.Call.URL != "https://www.fda.gov/consumers/ivermectin" || !strings.Contains(fetch.Content, "Why You Should Not Use Ivermectin") {
		t.Errorf("fetch evidence = %+v", fetch)
	}

	var meta struct {
		Backend       string `json:"backend"`
		Provider      string `json:"provider"`
		Model         string `json:"model"`
		ToolCalls     int    `json:"tool_calls"`
		ToolErrors    int    `json:"tool_errors"`
		EvidenceItems int    `json:"evidence_items"`
		Usage         struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(g.Metadata()), &meta); err != nil {
		t.Fatalf("Metadata() not JSON: %v (%q)", err, g.Metadata())
	}
	if meta.Backend != "goose" || meta.Provider != "openai" || meta.Model != "glm-test" || meta.ToolCalls != 3 || meta.ToolErrors != 1 || meta.EvidenceItems != 2 || meta.Usage.TotalTokens != 3873 {
		t.Errorf("metadata = %+v", meta)
	}
}

// Without web tools in the request goose gets no extension at all, and a
// blank provider and model leave goose's own config in charge.
func TestGoose_Complete_NoWebToolsNoExtension(t *testing.T) {
	binary, dir := fakeGoose(t, gooseAnswer("m1", "plain answer"), 0)
	g := &Goose{Binary: binary}
	got, err := g.Complete(context.Background(), Request{UserPrompt: "summarize"})
	if err != nil || got != "plain answer" {
		t.Fatalf("Complete() = %q, %v", got, err)
	}
	args := gooseArgs(t, dir)
	for _, flag := range []string{"--with-extension", "--provider", "--model", "--system"} {
		if argAfter(args, flag) != "" {
			t.Errorf("args %v should not have %s", args, flag)
		}
	}
	if got := argAfter(args, "--max-turns"); got != fmt.Sprint(defaultGooseTurns) {
		t.Errorf("--max-turns = %q, want the default %d", got, defaultGooseTurns)
	}
	if len(g.Evidence()) != 0 {
		t.Errorf("Evidence() = %v, want none", g.Evidence())
	}
}

// Text the agent wrote before a tool call is narration, not the answer: a
// run that ends on a tool result has no final message.
func TestGoose_Complete_NoFinalMessageAfterTools(t *testing.T) {
	binary, _ := fakeGoose(t, []string{gooseTextBefore, gooseSearchReq, gooseSearchResp, gooseComplete}, 0)
	g := &Goose{Binary: binary, WebCommand: "rg mcp --profile web"}
	_, err := g.Complete(context.Background(), Request{UserPrompt: "q", Tools: tools.DefaultTools()})
	if err == nil || !strings.Contains(err.Error(), "goose returned no final message") {
		t.Fatalf("err = %v, want no final message", err)
	}
	if len(g.Evidence()) != 1 {
		t.Errorf("the search result should still be kept as evidence, got %d", len(g.Evidence()))
	}
}

func TestGoose_Complete_ExitErrorIncludesStderr(t *testing.T) {
	binary, _ := fakeGoose(t, gooseAnswer("m1", "partial"), 3)
	_, err := (&Goose{Binary: binary}).Complete(context.Background(), Request{UserPrompt: "q"})
	if err == nil || !strings.Contains(err.Error(), "goose CLI failed") || !strings.Contains(err.Error(), "goose noise on stderr") {
		t.Fatalf("err = %v, want the exit error with stderr", err)
	}
}

func TestGoose_Complete_ErrorEvent(t *testing.T) {
	for name, event := range map[string]string{
		"string": `{"type":"error","error":"provider returned 429"}`,
		"object": `{"type":"error","error":{"message":"provider returned 429"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			binary, _ := fakeGoose(t, append(gooseAnswer("m1", "ignored"), event), 0)
			g := &Goose{Binary: binary}
			_, err := g.Complete(context.Background(), Request{UserPrompt: "q"})
			if err == nil || !strings.Contains(err.Error(), "goose run failed: provider returned 429") {
				t.Fatalf("err = %v", err)
			}
			if !strings.Contains(g.Metadata(), "provider returned 429") {
				t.Errorf("metadata %q should record the failure", g.Metadata())
			}
		})
	}
}

func TestGoose_WebCommandDefaultsToThisBinary(t *testing.T) {
	cmd, err := (&Goose{}).webCommand()
	if err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	if strings.ContainsAny(self, " \t") {
		t.Skip("test binary path has a space")
	}
	if cmd != self+" mcp --profile web" {
		t.Errorf("webCommand() = %q, want %q", cmd, self+" mcp --profile web")
	}
}

func TestGooseToolName(t *testing.T) {
	for in, want := range map[string]string{
		"researchguy__web_search": "web_search",
		"a__b__web_fetch":         "web_fetch",
		"web_fetch":               "web_fetch",
	} {
		if got := gooseToolName(in); got != want {
			t.Errorf("gooseToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewProvider_Goose(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Goose = config.GooseConfig{Binary: "/bin/goose", Provider: "openai", Model: "glm-cfg", MaxTurns: 9}
	p, err := NewProvider(cfg, "goose", "")
	if err != nil {
		t.Fatal(err)
	}
	g, ok := p.(*Goose)
	if !ok {
		t.Fatalf("NewProvider(goose) = %T", p)
	}
	if g.Binary != "/bin/goose" || g.Provider != "openai" || g.Model != "glm-cfg" || g.MaxTurns != 9 {
		t.Errorf("Goose = %+v", g)
	}
	if p, _ = NewProvider(cfg, "goose", "glm-override"); p.(*Goose).Model != "glm-override" {
		t.Errorf("Model = %q, want the override", p.(*Goose).Model)
	}
}

// A blank goose model is still a worker: goose runs its own configured one.
func TestHybridResolveWorkerModels_Goose(t *testing.T) {
	cfg := defaultTestConfig()
	h := &Hybrid{cfg: cfg}
	if got := h.resolveWorkerModels("goose"); len(got) != 1 || got[0] != "" {
		t.Errorf("goose with no model = %q, want one worker on goose's default", got)
	}
	cfg.Goose.Model = "glm-cfg"
	if got := h.resolveWorkerModels("goose"); len(got) != 1 || got[0] != "glm-cfg" {
		t.Errorf("goose fallback = %v, want [glm-cfg]", got)
	}
}

// TestHybrid_GooseWorkersClaudeAggregator runs the real provider wiring with
// fake goose and claude binaries: the goose search results reach the
// aggregator's ledger and the run's captures.
func TestHybrid_GooseWorkersClaudeAggregator(t *testing.T) {
	events := append([]string{gooseSearchReq, gooseSearchResp}, gooseAnswer("m2", "worker finding")...)
	gooseBin, gooseDir := fakeGoose(t, events, 0)

	claudeDir := t.TempDir()
	claudeBin := filepath.Join(claudeDir, "fake-claude")
	script := "#!/bin/sh\ncat > \"" + claudeDir + "/prompt.txt\"\necho 'aggregated report'\n"
	if err := os.WriteFile(claudeBin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	cfg := defaultTestConfig()
	cfg.DefaultBackend = "hybrid"
	cfg.Goose = config.GooseConfig{Binary: gooseBin}
	cfg.Claude.Binary = claudeBin
	cfg.Hybrid = config.HybridConfig{
		WorkerBackend:     "goose",
		AggregatorBackend: "claude",
		AggregatorModel:   "opus",
		MaxParallel:       2,
	}
	p, err := NewProvider(cfg, "", "")
	if err != nil {
		t.Fatal(err)
	}
	run := openRun(t)
	got, err := p.Complete(context.Background(), Request{UserPrompt: "topic", Mode: "inquiry", Tools: tools.DefaultTools(), Run: run})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got != "aggregated report" {
		t.Errorf("Complete() = %q, want the aggregator output", got)
	}
	prompt := readFile(t, filepath.Join(claudeDir, "prompt.txt"))
	if !strings.Contains(prompt, "worker finding") || !strings.Contains(prompt, "https://x.com/US_FDA/status/1429050070243192839") {
		t.Errorf("aggregator prompt missing goose drafts or evidence:\n%s", prompt)
	}
	captures, err := store.ReadCaptures(run.Dir())
	if err != nil || len(captures) != len(branchSets["inquiry"]) {
		t.Fatalf("run captured %d items (%v), want one per goose worker", len(captures), err)
	}
	if args := gooseArgs(t, gooseDir); !strings.HasPrefix(argAfter(args, "--with-extension"), gooseExtension+":") {
		t.Errorf("goose args %v should add the researchguy web extension", args)
	}
}
