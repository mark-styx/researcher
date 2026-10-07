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
prev=""
for a in "$@"; do
  if [ "$prev" = "--recipe" ]; then cp "$a" "$dir/recipe.json"; fi
  prev="$a"
done
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

// readGooseRecipe reads the recipe file a fake goose was given, raw and parsed.
func readGooseRecipe(t *testing.T, path string) (raw string, r struct{ Instructions, Prompt string }) {
	t.Helper()
	raw = readFile(t, path)
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("recipe %s is not JSON: %v", path, err)
	}
	return raw, r
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
		"--with-extension": "researchguy:/opt/rg mcp --profile web",
	} {
		if got := argAfter(args, flag); got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
	if argAfter(args, "--system") != "" || argAfter(args, "-i") != "" {
		t.Errorf("args %q should carry no prompt", args)
	}
	_, r := readGooseRecipe(t, filepath.Join(dir, "recipe.json"))
	if r.Instructions != "Be rigorous.\n\n"+gooseBudget(12) {
		t.Errorf("recipe instructions = %q, want the system prompt and then the turn budget", r.Instructions)
	}
	if r.Prompt != "When did the FDA post it?" {
		t.Errorf("recipe prompt = %q, want the user prompt alone", r.Prompt)
	}
	if stdin := readFile(t, filepath.Join(dir, "stdin.txt")); stdin != "" {
		t.Errorf("stdin = %q, want nothing", stdin)
	}
	recipe := argAfter(args, "--recipe")
	if _, err := os.Stat(recipe); !os.IsNotExist(err) {
		t.Errorf("recipe %q should be removed after the run", recipe)
	}
	cwd := strings.TrimSpace(readFile(t, filepath.Join(dir, "cwd.txt")))
	if !strings.Contains(filepath.Base(cwd), "researchguy-goose-") {
		t.Errorf("goose ran in %q, want an isolated scratch dir", cwd)
	}
	if filepath.Dir(recipe) == cwd {
		t.Errorf("recipe %q should sit outside the scratch dir", recipe)
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
	if raw, r := readGooseRecipe(t, filepath.Join(dir, "recipe.json")); strings.Contains(raw, `"instructions"`) || r.Prompt != "summarize" {
		t.Errorf("recipe = %s, want the prompt and no instructions", raw)
	}
	if got := argAfter(args, "--max-turns"); got != fmt.Sprint(defaultGooseTurns) {
		t.Errorf("--max-turns = %q, want the default %d", got, defaultGooseTurns)
	}
	if len(g.Evidence()) != 0 {
		t.Errorf("Evidence() = %v, want none", g.Evidence())
	}
}

// A system prompt past ARG_MAX (1 MiB on macOS, 128 KiB for one argument
// on Linux) used to go on argv as --system, and every worker failed with
// "argument list too long". Both prompts go in the recipe file instead.
func TestGoose_Complete_LongPromptsStayOffArgv(t *testing.T) {
	binary, dir := fakeGoose(t, gooseAnswer("m1", "done"), 0)
	system := strings.Repeat("Existing research, whole files and chunks.\n", 50000) + "end"
	prompt := strings.Repeat("The topic, with its rules. ", 50000) + "end"
	g := &Goose{Binary: binary}
	got, err := g.Complete(context.Background(), Request{SystemPrompt: system, UserPrompt: prompt})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got != "done" {
		t.Errorf("Complete() = %q, want done", got)
	}
	for _, a := range gooseArgs(t, dir) {
		if len(a) > 4096 {
			t.Fatalf("an argument of %d bytes went on argv", len(a))
		}
	}
	_, r := readGooseRecipe(t, filepath.Join(dir, "recipe.json"))
	if r.Instructions != system || r.Prompt != prompt {
		t.Errorf("recipe has %d bytes of instructions and %d of prompt, want %d and %d", len(r.Instructions), len(r.Prompt), len(system), len(prompt))
	}
}

// Text the agent wrote before a tool call is narration, not the answer: a
// run that ends on a tool result has no final message. With nothing
// gathered either, the worker fails.
func TestGoose_Complete_NoFinalMessageNoEvidence(t *testing.T) {
	binary, _ := fakeGoose(t, []string{gooseTextBefore, gooseFailedReq, gooseFailedResp, gooseComplete}, 0)
	g := &Goose{Binary: binary, WebCommand: "rg mcp --profile web"}
	_, err := g.Complete(context.Background(), Request{UserPrompt: "q", Tools: tools.DefaultTools()})
	if err == nil || !strings.Contains(err.Error(), "goose returned no final message") {
		t.Fatalf("err = %v, want no final message", err)
	}
}

// fakeGooseTwoPass stands in for goose across a run and its finish pass: a
// call with the web extension prints toolEvents, one without prints
// finishEvents. Each call's argv, stdin and recipe go to args.<n>.txt,
// stdin.<n>.txt and recipe.<n>.json under dir, numbered from 1.
func fakeGooseTwoPass(t *testing.T, toolEvents, finishEvents []string) (binary, dir string) {
	t.Helper()
	dir = t.TempDir()
	for name, events := range map[string][]string{"tools.jsonl": toolEvents, "finish.jsonl": finishEvents} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(events, "\n")+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
dir="` + dir + `"
n=$(( $(cat "$dir/count" 2>/dev/null || echo 0) + 1 ))
echo "$n" > "$dir/count"
printf '%s\n' "$@" > "$dir/args.$n.txt"
cat > "$dir/stdin.$n.txt"
prev=""
for a in "$@"; do
  if [ "$prev" = "--recipe" ]; then cp "$a" "$dir/recipe.$n.json"; fi
  prev="$a"
done
echo '    __( O)>  new session'
case " $* " in
  *" --with-extension "*) cat "$dir/tools.jsonl" ;;
  *) cat "$dir/finish.jsonl" ;;
esac
`
	binary = filepath.Join(dir, "fake-goose")
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return binary, dir
}

const gooseTurnLimitMsg = `{"type":"message","message":{"id":"m9","role":"assistant","content":[{"type":"text","text":"I've reached the maximum number of actions I can do without user input. Would you like me to continue?"}]}}`

type gooseFinishMeta struct {
	ToolCalls     int    `json:"tool_calls"`
	EvidenceItems int    `json:"evidence_items"`
	TurnLimit     bool   `json:"turn_limit"`
	FinishPass    string `json:"finish_pass"`
	FinishUsage   *struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"finish_usage"`
}

func finishMeta(t *testing.T, g *Goose) gooseFinishMeta {
	t.Helper()
	var m gooseFinishMeta
	if err := json.Unmarshal([]byte(g.Metadata()), &m); err != nil {
		t.Fatalf("Metadata() not JSON: %v (%q)", err, g.Metadata())
	}
	return m
}

// A run that ends on goose's turn-limit notice has gathered evidence but
// written nothing. One more call, with no tools, writes the answer from
// what it gathered.
func TestGoose_Complete_TurnLimitFinishPass(t *testing.T) {
	toolEvents := []string{gooseSearchReq, gooseSearchResp, gooseFetchReq, gooseFetchResp, gooseTurnLimitMsg, gooseComplete}
	finishEvents := append(gooseAnswer("f1", "From the results: ", "the FDA posted it."), `{"type":"complete","total_tokens":900,"input_tokens":800,"output_tokens":100}`)
	binary, dir := fakeGooseTwoPass(t, toolEvents, finishEvents)

	var captured int
	g := &Goose{Binary: binary, Model: "glm-test", MaxTurns: 30, WebCommand: "rg mcp --profile web"}
	got, err := g.Complete(context.Background(), Request{
		SystemPrompt: "Be rigorous.",
		UserPrompt:   "When did the FDA post it?",
		Tools:        tools.DefaultTools(),
		Capture:      func(EvidenceRecord) string { captured++; return fmt.Sprintf("E%d", captured) },
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got != "From the results: the FDA posted it." {
		t.Errorf("Complete() = %q, want the finish pass's answer", got)
	}
	if n := strings.TrimSpace(readFile(t, filepath.Join(dir, "count"))); n != "2" {
		t.Fatalf("goose ran %s times, want 2", n)
	}
	args := strings.Split(strings.TrimSpace(readFile(t, filepath.Join(dir, "args.2.txt"))), "\n")
	if argAfter(args, "--with-extension") != "" {
		t.Errorf("finish pass args %v should have no tools", args)
	}
	if argAfter(args, "--max-turns") != "2" || argAfter(args, "--model") != "glm-test" {
		t.Errorf("finish pass args = %v, want 2 turns and the same model", args)
	}
	_, r := readGooseRecipe(t, filepath.Join(dir, "recipe.2.json"))
	if r.Instructions != "Be rigorous." {
		t.Errorf("finish pass instructions = %q, want the plain system prompt", r.Instructions)
	}
	for _, want := range []string{"When did the FDA post it?", "Tool result 1: web_search", "You are not a horse.", "Tool result 2: web_fetch", "Why You Should Not Use Ivermectin", "used up"} {
		if !strings.Contains(r.Prompt, want) {
			t.Errorf("finish prompt missing %q:\n%s", want, r.Prompt)
		}
	}
	if len(g.Evidence()) != 2 || captured != 2 {
		t.Errorf("evidence = %d, captured %d, want the first run's 2 (the finish pass captures nothing)", len(g.Evidence()), captured)
	}
	m := finishMeta(t, g)
	if !m.TurnLimit || m.FinishPass != "ok" || m.FinishUsage == nil || m.FinishUsage.TotalTokens != 900 || m.ToolCalls != 2 || m.EvidenceItems != 2 {
		t.Errorf("metadata = %+v (%s)", m, g.Metadata())
	}
}

// A run that ends on a tool result also gets the finish pass.
func TestGoose_Complete_NoFinalMessageFinishPass(t *testing.T) {
	binary, _ := fakeGooseTwoPass(t, []string{gooseTextBefore, gooseSearchReq, gooseSearchResp, gooseComplete}, gooseAnswer("f1", "answer from evidence"))
	g := &Goose{Binary: binary, WebCommand: "rg mcp --profile web"}
	got, err := g.Complete(context.Background(), Request{UserPrompt: "q", Tools: tools.DefaultTools()})
	if err != nil || got != "answer from evidence" {
		t.Fatalf("Complete() = %q, %v", got, err)
	}
	if m := finishMeta(t, g); m.TurnLimit || m.FinishPass != "ok" {
		t.Errorf("metadata = %+v, want a finish pass without the turn-limit flag", m)
	}
}

// If the finish pass fails too, the worker still succeeds with a draft
// that says so, because a failed worker's evidence leaves the ledger.
func TestGoose_Complete_FinishPassFails(t *testing.T) {
	for name, finishEvents := range map[string][]string{
		"no answer":  {gooseComplete},
		"turn limit": {gooseTurnLimitMsg},
		"error":      {`{"type":"error","error":"provider returned 500"}`},
	} {
		t.Run(name, func(t *testing.T) {
			binary, _ := fakeGooseTwoPass(t, []string{gooseSearchReq, gooseSearchResp, gooseTurnLimitMsg}, finishEvents)
			g := &Goose{Binary: binary, WebCommand: "rg mcp --profile web"}
			got, err := g.Complete(context.Background(), Request{UserPrompt: "q", Tools: tools.DefaultTools()})
			if err != nil {
				t.Fatalf("Complete() error: %v", err)
			}
			if got != fmt.Sprintf(gooseNoAnswerDraft, 1) {
				t.Errorf("Complete() = %q, want the no-answer draft", got)
			}
			if hitTurnLimit(got) {
				t.Errorf("the no-answer draft should not read as goose's notice")
			}
			m := finishMeta(t, g)
			if !m.TurnLimit || m.FinishPass == "ok" || m.FinishPass == "" {
				t.Errorf("metadata = %+v, want the finish error recorded", m)
			}
			if len(g.Evidence()) != 1 {
				t.Errorf("evidence = %d, want 1", len(g.Evidence()))
			}
		})
	}
}

// With no evidence there is nothing to write from: the notice is returned
// as goose gave it, in one call, and the hybrid no-evidence check fails the
// worker.
func TestGoose_Complete_TurnLimitNoEvidence(t *testing.T) {
	binary, dir := fakeGooseTwoPass(t, []string{gooseFailedReq, gooseFailedResp, gooseTurnLimitMsg}, gooseAnswer("f1", "should not run"))
	g := &Goose{Binary: binary, WebCommand: "rg mcp --profile web"}
	got, err := g.Complete(context.Background(), Request{UserPrompt: "q", Tools: tools.DefaultTools()})
	if err != nil || !hitTurnLimit(got) {
		t.Fatalf("Complete() = %q, %v, want goose's notice", got, err)
	}
	if n := strings.TrimSpace(readFile(t, filepath.Join(dir, "count"))); n != "1" {
		t.Errorf("goose ran %s times, want 1", n)
	}
	if m := finishMeta(t, g); m.FinishPass != "" {
		t.Errorf("finish_pass = %q, want none", m.FinishPass)
	}
}

func TestHitTurnLimit(t *testing.T) {
	for text, want := range map[string]bool{
		"I've reached the maximum number of actions I can do without user input. Would you like me to continue?": true,
		"  I\u2019ve reached the maximum number of actions I can do":                                             true,
		"The FDA posted it.": false,
		"":                   false,
		"Note: I've reached the maximum number of actions": false,
	} {
		if got := hitTurnLimit(text); got != want {
			t.Errorf("hitTurnLimit(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestFinishPrompt_Caps(t *testing.T) {
	big := strings.Repeat("x", maxGooseFinishItemChars+500)
	var ev []EvidenceRecord
	for i := 0; i < 40; i++ {
		ev = append(ev, EvidenceRecord{Label: fmt.Sprintf("web_fetch {\"url\":\"https://e.example/%d\"}", i), Content: big})
	}
	p := finishPrompt("task", ev)
	if !strings.HasPrefix(p, "task\n") {
		t.Errorf("prompt should start with the task")
	}
	if len(p) > maxGooseFinishChars+2000 {
		t.Errorf("prompt is %d chars, want about %d at most", len(p), maxGooseFinishChars)
	}
	if !strings.Contains(p, "[truncated]") || !strings.Contains(p, "more tool results left out for length") {
		t.Errorf("prompt should mark truncated items and left-out ones")
	}
	if strings.Contains(finishPrompt("task", nil), "Tool result") {
		t.Errorf("no evidence should list no tool results")
	}
}

func TestSetArg(t *testing.T) {
	got := setArg([]string{"run", "--max-turns", "30", "--no-session"}, "--max-turns", "2")
	if strings.Join(got, " ") != "run --max-turns 2 --no-session" {
		t.Errorf("replace = %v", got)
	}
	got = setArg([]string{"run", "--no-session"}, "--max-turns", "2")
	if strings.Join(got, " ") != "run --no-session --max-turns 2" {
		t.Errorf("insert = %v", got)
	}
	orig := []string{"run", "--max-turns", "30"}
	setArg(orig, "--max-turns", "2")
	if orig[2] != "30" {
		t.Errorf("setArg should not change its input")
	}
}

// goose renders a recipe file as a template before parsing it. Every brace
// in the text is a JSON escape, so the file's only braces are its own, and
// the text parses back unchanged.
func TestGooseRecipe_BracesEscaped(t *testing.T) {
	system := "Report says {{ name }} and {% raw %}x{% endraw %} {# note #}.\nQuote: \"a\" \u2014 \U000E0041"
	prompt := "Topic {x} and }} {{"
	raw := string(gooseRecipe(system, prompt))
	if strings.Count(raw, "{") != 1 || strings.Count(raw, "}") != 1 {
		t.Errorf("recipe has braces from the text: %s", raw)
	}
	var r struct{ Version, Title, Description, Instructions, Prompt string }
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("recipe not JSON: %v", err)
	}
	if r.Instructions != system || r.Prompt != prompt {
		t.Errorf("round trip = %q, %q", r.Instructions, r.Prompt)
	}
	if r.Version == "" || r.Title == "" || r.Description == "" {
		t.Errorf("recipe lacks the fields goose requires: %+v", r)
	}
	if raw := string(gooseRecipe("", "p")); strings.Contains(raw, "instructions") {
		t.Errorf("an empty system prompt should leave instructions out: %s", raw)
	}
}

func TestGooseBudget(t *testing.T) {
	if b := gooseBudget(30); !strings.Contains(b, "at most 30 turns") || !strings.Contains(b, "after about 25") {
		t.Errorf("gooseBudget(30) = %q", b)
	}
	if b := gooseBudget(3); !strings.Contains(b, "after about 1 ") {
		t.Errorf("gooseBudget(3) = %q, want a floor of 1", b)
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
