package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/marklubin/researchguy/internal/tools"
)

const (
	maxGooseStderrChars = 4000
	defaultGooseTurns   = 40
	// gooseExtension names the web extension, so its tools arrive as
	// researchguy__web_search and researchguy__web_fetch.
	gooseExtension = "researchguy"
)

// Goose shells out to the Goose CLI ("goose run") for completions. It runs
// without the user's profile, so none of their extensions load (researchguy
// among them, and the developer shell). With web tools in the request it
// adds `researchguy mcp --profile web` as its only extension, and keeps the
// pages and search results the agent saw as evidence for the hybrid ledger.
// The system prompt and the prompt go in a recipe file, never on argv.
type Goose struct {
	Binary   string
	Provider string // blank = Goose's own configured provider
	Model    string // blank = Goose's own configured model
	MaxTurns int
	// WebCommand is the command line for the web extension. Blank runs this
	// researchguy binary with `mcp --profile web`.
	WebCommand string

	mu           sync.RWMutex
	lastEvidence []EvidenceRecord
	lastMetadata string
}

func (g *Goose) Name() string {
	return "goose"
}

func (g *Goose) Metadata() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.lastMetadata
}

func (g *Goose) Evidence() []EvidenceRecord {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]EvidenceRecord(nil), g.lastEvidence...)
}

func (g *Goose) Complete(ctx context.Context, req Request) (string, error) {
	g.setRun(nil, "")

	args, err := g.args(req)
	if err != nil {
		return "", err
	}
	// Events are read as they arrive, so each tool result is captured to
	// the run before the agent goes on, not when it exits.
	run, err := g.exec(ctx, args, g.system(req), req.UserPrompt, req.Capture)
	g.setRun(run.evidence, run.metadataJSON(g))
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(run.lastMessage)
	if (text == "" || hitTurnLimit(text)) && len(run.evidence) > 0 {
		// The agent spent its turns on tools and never answered. Its tool
		// results are the evidence either way, so one more call without
		// tools writes the answer from them.
		run.turnLimit = hitTurnLimit(text)
		run.finishRan = true
		answer, fin, ferr := g.finish(ctx, req, run.evidence)
		run.finishUsage = fin.usage
		if ferr != nil {
			run.finishErr = ferr.Error()
		}
		g.setRun(run.evidence, run.metadataJSON(g))
		if ferr == nil {
			return answer, nil
		}
		// A failed worker's evidence is dropped from the ledger, so the
		// worker still succeeds, with a draft that says what happened.
		return fmt.Sprintf(gooseNoAnswerDraft, len(run.evidence)), nil
	}
	if text == "" {
		return "", fmt.Errorf("goose returned no final message")
	}
	return text, nil
}

// gooseTurnLimitNotice starts the message goose ends a run with when it
// runs out of turns. The run has no answer then.
const gooseTurnLimitNotice = "I've reached the maximum number of actions"

func hitTurnLimit(text string) bool {
	text = strings.ReplaceAll(strings.TrimSpace(text), "’", "'")
	return strings.HasPrefix(text, gooseTurnLimitNotice)
}

const gooseNoAnswerDraft = "(No findings: this worker ran out of turns before writing them, and the pass to write them from its tool results failed. Its %d tool results are in the evidence ledger.)"

// maxGooseFinishChars caps the tool results the finish pass reads, and
// maxGooseFinishItemChars caps any one of them.
const (
	maxGooseFinishChars     = 120000
	maxGooseFinishItemChars = 8000
)

// finish asks goose, with no tools, for the answer to req from the tool
// results a run gathered before it ran out of turns.
func (g *Goose) finish(ctx context.Context, req Request, evidence []EvidenceRecord) (string, gooseRun, error) {
	finReq := req
	finReq.Tools = nil
	args, err := g.args(finReq)
	if err != nil {
		return "", gooseRun{}, err
	}
	args = setArg(args, "--max-turns", "2")
	run, err := g.exec(ctx, args, g.system(finReq), finishPrompt(req.UserPrompt, evidence), nil)
	if err != nil {
		return "", run, fmt.Errorf("finish pass: %w", err)
	}
	text := strings.TrimSpace(run.lastMessage)
	if text == "" || hitTurnLimit(text) {
		return "", run, fmt.Errorf("finish pass returned no answer")
	}
	return text, run, nil
}

// finishPrompt is the task again with the tool results, in the order they
// arrived, under a cap.
func finishPrompt(task string, evidence []EvidenceRecord) string {
	var b strings.Builder
	b.WriteString(task)
	b.WriteString("\n\nYour tool budget for this task is used up, and you can't search or fetch anything more. Write your answer now, from the tool results below only. Say what they show, what they don't, and what you could not find or read.\n\n")
	used := 0
	for i, rec := range evidence {
		content := rec.Content
		if len(content) > maxGooseFinishItemChars {
			content = content[:maxGooseFinishItemChars] + "\n[truncated]"
		}
		item := fmt.Sprintf("--- Tool result %d: %s\n%s\n\n", i+1, rec.Label, content)
		if used+len(item) > maxGooseFinishChars {
			fmt.Fprintf(&b, "[%d more tool results left out for length]\n", len(evidence)-i)
			break
		}
		b.WriteString(item)
		used += len(item)
	}
	return b.String()
}

// setArg replaces flag's value in args, or appends the flag.
func setArg(args []string, flag, value string) []string {
	out := append([]string(nil), args...)
	for i, a := range out {
		if a == flag && i+1 < len(out) {
			out[i+1] = value
			return out
		}
	}
	return append(out, flag, value)
}

// exec runs goose once with args, and system and prompt in a recipe file,
// in an empty scratch dir.
func (g *Goose) exec(ctx context.Context, args []string, system, prompt string, capture CaptureFunc) (gooseRun, error) {
	workDir, err := os.MkdirTemp("", "researchguy-goose-")
	if err != nil {
		return gooseRun{}, fmt.Errorf("creating goose work dir: %w", err)
	}
	defer os.RemoveAll(workDir)
	// The recipe sits outside the scratch dir, so that stays empty.
	recipe, err := writeGooseRecipe(system, prompt)
	if err != nil {
		return gooseRun{}, err
	}
	defer os.Remove(recipe)

	binary := g.Binary
	if binary == "" {
		binary = "goose"
	}
	cmd := exec.CommandContext(ctx, binary, append(append([]string(nil), args...), "--recipe", recipe)...)
	// An empty scratch dir keeps the agent away from project files and any
	// .goosehints where researchguy was launched.
	cmd.Dir = workDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return gooseRun{}, fmt.Errorf("goose stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return gooseRun{}, fmt.Errorf("starting goose CLI: %w", err)
	}
	parser := gooseParser{capture: capture}
	readErr := parser.read(stdout)
	runErr := cmd.Wait()
	run := parser.run
	if runErr == nil && readErr != nil {
		runErr = fmt.Errorf("reading goose output: %w", readErr)
	}
	if run.failure != "" {
		return run, fmt.Errorf("goose run failed: %s", run.failure)
	}
	if runErr != nil {
		return run, fmt.Errorf("goose CLI failed: %w\nstderr: %s", runErr, tailString(stderr.String(), maxGooseStderrChars))
	}
	return run, nil
}

func (g *Goose) turns() int {
	if g.MaxTurns <= 0 {
		return defaultGooseTurns
	}
	return g.MaxTurns
}

// args is the command line for req, less the recipe.
func (g *Goose) args(req Request) ([]string, error) {
	args := []string{
		"run",
		"--no-session",
		"--no-profile",
		"--output-format", "stream-json",
		"--max-turns", fmt.Sprint(g.turns()),
		"--max-tool-repetitions", "3",
	}
	if g.Provider != "" {
		args = append(args, "--provider", g.Provider)
	}
	if g.Model != "" {
		args = append(args, "--model", g.Model)
	}
	if wantsWebTools(req) {
		web, err := g.webCommand()
		if err != nil {
			return nil, err
		}
		args = append(args, "--with-extension", gooseExtension+":"+web)
	}
	return args, nil
}

// system is req's system prompt, plus the turn budget when the agent has
// web tools.
func (g *Goose) system(req Request) string {
	system := strings.TrimSpace(req.SystemPrompt)
	if wantsWebTools(req) {
		// Agents otherwise call tools until goose stops them, and the run
		// ends on goose's turn-limit notice instead of an answer.
		system = strings.TrimSpace(system + "\n\n" + gooseBudget(g.turns()))
	}
	return system
}

// writeGooseRecipe writes a recipe file for one run and returns its path.
// goose has no file form of --system, and a dive's system prompt carries
// its research context, which can pass ARG_MAX (1 MiB on macOS) on argv.
// A recipe can't be combined with -i, so the prompt goes in it too.
func writeGooseRecipe(system, prompt string) (string, error) {
	f, err := os.CreateTemp("", "researchguy-goose-recipe-*.json")
	if err != nil {
		return "", fmt.Errorf("creating goose recipe: %w", err)
	}
	_, werr := f.Write(gooseRecipe(system, prompt))
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("writing goose recipe: %w", werr)
	}
	return f.Name(), nil
}

// gooseRecipe is the recipe: the system prompt as its instructions, and
// the prompt. goose renders a recipe file as a template before parsing it,
// so every brace in the text is written as a JSON escape, and a "{{" in a
// fetched page or a report reaches the model as written, not as a tag.
func gooseRecipe(system, prompt string) []byte {
	var b bytes.Buffer
	b.WriteString(`{"version":"1.0.0","title":"researchguy","description":"one researchguy goose run"`)
	if system != "" {
		b.WriteString(`,"instructions":`)
		b.Write(gooseJSONString(system))
	}
	b.WriteString(`,"prompt":`)
	b.Write(gooseJSONString(prompt))
	b.WriteString("}")
	return b.Bytes()
}

func gooseJSONString(s string) []byte {
	out, _ := json.Marshal(s) // a string always marshals
	out = bytes.ReplaceAll(out, []byte("{"), []byte(`\u007b`))
	return bytes.ReplaceAll(out, []byte("}"), []byte(`\u007d`))
}

// gooseBudget tells the agent how many turns it has, leaving a few to
// write its answer.
func gooseBudget(turns int) string {
	stop := turns - 5
	if stop < 1 {
		stop = 1
	}
	return fmt.Sprintf("You have at most %d turns for this task, and each tool call uses one. Stop calling tools after about %d and write your answer; a run that reaches the limit ends with no answer.", turns, stop)
}

func (g *Goose) webCommand() (string, error) {
	if g.WebCommand != "" {
		return g.WebCommand, nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("finding the researchguy binary for goose's web tools: %w", err)
	}
	// Goose splits the extension command on spaces.
	if strings.ContainsAny(self, " \t") {
		return "", fmt.Errorf("researchguy binary path %q has a space; set the goose web command to a path without one", self)
	}
	return self + " mcp --profile web", nil
}

func (g *Goose) setRun(evidence []EvidenceRecord, metadata string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastEvidence = evidence
	g.lastMetadata = metadata
}

// --- goose run --output-format stream-json ---

type gooseEvent struct {
	Type    string          `json:"type"`
	Message *gooseMessage   `json:"message"`
	Error   json.RawMessage `json:"error"`

	TotalTokens  int `json:"total_tokens"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type gooseMessage struct {
	ID       string         `json:"id"`
	Role     string         `json:"role"`
	Content  []gooseContent `json:"content"`
	Metadata struct {
		Inference struct {
			Provider       string `json:"provider"`
			RequestedModel string `json:"requestedModel"`
		} `json:"inference"`
	} `json:"metadata"`
}

type gooseContent struct {
	Type       string           `json:"type"`
	Text       string           `json:"text"`
	ID         string           `json:"id"`
	ToolCall   *gooseToolCall   `json:"toolCall"`
	ToolResult *gooseToolResult `json:"toolResult"`
}

type gooseToolCall struct {
	Status string `json:"status"`
	Value  struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"value"`
}

type gooseToolResult struct {
	Status string          `json:"status"`
	Error  json.RawMessage `json:"error"`
	Value  struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	} `json:"value"`
}

type gooseUsage struct {
	TotalTokens  int `json:"total_tokens"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type gooseRun struct {
	lastMessage string
	evidence    []EvidenceRecord
	toolCalls   int
	toolErrors  int
	provider    string
	model       string
	usage       *gooseUsage
	failure     string

	// Set when the run ended without an answer and a finish pass ran.
	turnLimit   bool
	finishRan   bool
	finishUsage *gooseUsage
	finishErr   string
}

// gooseParser reads goose's stream-json events. capture, when set, gets
// each successful web tool result as its response is read.
type gooseParser struct {
	run     gooseRun
	capture CaptureFunc
	pending map[string]tools.ToolCall // tool requests by id, until answered

	// Goose streams text as deltas that share a message id. The answer is
	// the last assistant message's text; text before a tool call isn't.
	textID string
	text   strings.Builder
}

// read feeds every line of r to the parser. It reads to EOF whatever the
// lines hold, so goose never blocks on a full pipe.
func (p *gooseParser) read(r io.Reader) error {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			p.feed(line)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (p *gooseParser) feed(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return // goose prints a banner before the events
	}
	var ev gooseEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}
	run := &p.run
	switch ev.Type {
	case "message":
		if ev.Message != nil {
			p.message(ev.Message)
		}
	case "complete":
		run.usage = &gooseUsage{TotalTokens: ev.TotalTokens, InputTokens: ev.InputTokens, OutputTokens: ev.OutputTokens}
	case "error":
		if run.failure == "" {
			run.failure = gooseErrorText(ev.Error)
			if run.failure == "" {
				run.failure = string(line)
			}
		}
	}
}

func (p *gooseParser) message(m *gooseMessage) {
	run := &p.run
	if inf := m.Metadata.Inference; inf.RequestedModel != "" {
		run.provider, run.model = inf.Provider, inf.RequestedModel
	}
	for _, c := range m.Content {
		switch c.Type {
		case "text":
			if m.Role != "assistant" {
				continue
			}
			if m.ID != p.textID {
				p.text.Reset()
				p.textID = m.ID
			}
			p.text.WriteString(c.Text)
			run.lastMessage = p.text.String()
		case "toolRequest":
			p.text.Reset()
			p.textID = ""
			run.lastMessage = ""
			if c.ToolCall == nil {
				continue
			}
			run.toolCalls++
			if p.pending == nil {
				p.pending = make(map[string]tools.ToolCall)
			}
			p.pending[c.ID] = tools.ToolCall{
				Name:      gooseToolName(c.ToolCall.Value.Name),
				Arguments: tools.ParseToolArguments(c.ToolCall.Value.Arguments),
			}
		case "toolResponse":
			call, ok := p.pending[c.ID]
			delete(p.pending, c.ID)
			if !ok || c.ToolResult == nil {
				continue
			}
			p.toolResponse(call, c.ToolResult)
		}
	}
}

func (p *gooseParser) toolResponse(call tools.ToolCall, res *gooseToolResult) {
	run := &p.run
	if res.Status != "success" || res.Value.IsError {
		run.toolErrors++
		return
	}
	if call.Name != "web_search" && call.Name != "web_fetch" {
		return
	}
	var text []string
	for _, c := range res.Value.Content {
		if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
			text = append(text, c.Text)
		}
	}
	result := tools.ToolResult{Name: call.Name, Content: strings.Join(text, "\n")}
	if call.Name == "web_search" {
		result.Results = gooseSearchResults(res.Value.StructuredContent)
	}
	if strings.TrimSpace(result.Content) == "" {
		return
	}
	rec := EvidenceRecord{
		Label:   evidenceLabel(call),
		Content: result.Content,
		Call:    toolCallRecord(call, result),
	}
	if p.capture != nil {
		rec.ID = p.capture(rec)
	}
	run.evidence = append(run.evidence, rec)
}

// gooseToolName strips the extension prefix goose puts on a stdio
// extension's tools ("researchguy__web_search").
func gooseToolName(name string) string {
	if i := strings.LastIndex(name, "__"); i >= 0 {
		return name[i+2:]
	}
	return name
}

// gooseSearchResults reads the ranked results the web profile's web_search
// returns as structured content.
func gooseSearchResults(raw json.RawMessage) []tools.SearchResult {
	if len(raw) == 0 {
		return nil
	}
	var structured struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &structured); err != nil {
		return nil
	}
	out := make([]tools.SearchResult, 0, len(structured.Results))
	for _, r := range structured.Results {
		out = append(out, tools.SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Snippet})
	}
	return out
}

// gooseErrorText reads an error event's error, a string or an object with
// a message.
func gooseErrorText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Message != "" {
		return obj.Message
	}
	return string(raw)
}

// finishPass is "ok" or the error when a finish pass ran, else "".
func (r gooseRun) finishPass() string {
	switch {
	case !r.finishRan:
		return ""
	case r.finishErr != "":
		return truncateForMetadata(r.finishErr, 2000)
	}
	return "ok"
}

func (r gooseRun) metadataJSON(g *Goose) string {
	model := r.model
	if model == "" {
		model = g.Model
	}
	provider := r.provider
	if provider == "" {
		provider = g.Provider
	}
	meta := struct {
		Backend       string      `json:"backend"`
		Provider      string      `json:"provider,omitempty"`
		Model         string      `json:"model,omitempty"`
		ToolCalls     int         `json:"tool_calls"`
		ToolErrors    int         `json:"tool_errors"`
		EvidenceItems int         `json:"evidence_items"`
		Usage         *gooseUsage `json:"usage,omitempty"`
		Failure       string      `json:"failure,omitempty"`
		TurnLimit     bool        `json:"turn_limit,omitempty"`
		FinishPass    string      `json:"finish_pass,omitempty"`
		FinishUsage   *gooseUsage `json:"finish_usage,omitempty"`
	}{
		Backend:       "goose",
		Provider:      provider,
		Model:         model,
		ToolCalls:     r.toolCalls,
		ToolErrors:    r.toolErrors,
		EvidenceItems: len(r.evidence),
		Usage:         r.usage,
		Failure:       truncateForMetadata(r.failure, 2000),
		TurnLimit:     r.turnLimit,
		FinishPass:    r.finishPass(),
		FinishUsage:   r.finishUsage,
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(b)
}
