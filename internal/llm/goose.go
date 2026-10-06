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
	workDir, err := os.MkdirTemp("", "researchguy-goose-")
	if err != nil {
		return "", fmt.Errorf("creating goose work dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	binary := g.Binary
	if binary == "" {
		binary = "goose"
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	// An empty scratch dir keeps the agent away from project files and any
	// .goosehints where researchguy was launched.
	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(req.UserPrompt)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("goose stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting goose CLI: %w", err)
	}
	// Events are read as they arrive, so each tool result is captured to
	// the run before the agent goes on, not when it exits.
	parser := gooseParser{capture: req.Capture}
	readErr := parser.read(stdout)
	runErr := cmd.Wait()
	run := parser.run
	g.setRun(run.evidence, run.metadataJSON(g))
	if runErr == nil && readErr != nil {
		runErr = fmt.Errorf("reading goose output: %w", readErr)
	}

	if run.failure != "" {
		return "", fmt.Errorf("goose run failed: %s", run.failure)
	}
	if runErr != nil {
		return "", fmt.Errorf("goose CLI failed: %w\nstderr: %s", runErr, tailString(stderr.String(), maxGooseStderrChars))
	}
	text := strings.TrimSpace(run.lastMessage)
	if text == "" {
		return "", fmt.Errorf("goose returned no final message")
	}
	return text, nil
}

func (g *Goose) args(req Request) ([]string, error) {
	turns := g.MaxTurns
	if turns <= 0 {
		turns = defaultGooseTurns
	}
	args := []string{
		"run",
		"--no-session",
		"--no-profile",
		"--output-format", "stream-json",
		"--max-turns", fmt.Sprint(turns),
		"--max-tool-repetitions", "3",
	}
	if g.Provider != "" {
		args = append(args, "--provider", g.Provider)
	}
	if g.Model != "" {
		args = append(args, "--model", g.Model)
	}
	if s := strings.TrimSpace(req.SystemPrompt); s != "" {
		args = append(args, "--system", s)
	}
	if wantsWebTools(req) {
		web, err := g.webCommand()
		if err != nil {
			return nil, err
		}
		args = append(args, "--with-extension", gooseExtension+":"+web)
	}
	// The prompt goes on stdin so long prompts never hit argv limits.
	return append(args, "-i", "-"), nil
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
	}{
		Backend:       "goose",
		Provider:      provider,
		Model:         model,
		ToolCalls:     r.toolCalls,
		ToolErrors:    r.toolErrors,
		EvidenceItems: len(r.evidence),
		Usage:         r.usage,
		Failure:       truncateForMetadata(r.failure, 2000),
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(b)
}
