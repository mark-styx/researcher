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

	"github.com/marklubin/researchguy/internal/store"
)

const maxCodexStderrChars = 4000

// Codex shells out to the Codex CLI ("codex exec") for completions. It runs
// with --json so the raw web search results the agent saw are kept as
// evidence, which the hybrid aggregator puts in its ledger.
type Codex struct {
	Binary          string
	Model           string // blank = Codex's own default
	ReasoningEffort string // blank = Codex's own default
	// IgnoreUserConfig skips $CODEX_HOME/config.toml, so workers don't start
	// the user's MCP servers (researchguy among them). Auth still comes from
	// CODEX_HOME.
	IgnoreUserConfig bool

	mu           sync.RWMutex
	lastEvidence []EvidenceRecord
	lastMetadata string
}

func (c *Codex) Name() string {
	return "codex"
}

func (c *Codex) Metadata() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastMetadata
}

func (c *Codex) Evidence() []EvidenceRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]EvidenceRecord(nil), c.lastEvidence...)
}

func (c *Codex) Complete(ctx context.Context, req Request) (string, error) {
	c.setRun(nil, "")

	workDir, err := os.MkdirTemp("", "researchguy-codex-")
	if err != nil {
		return "", fmt.Errorf("creating codex work dir: %w", err)
	}
	defer os.RemoveAll(workDir)
	lastMessagePath := workDir + "/last-message.txt"

	binary := c.Binary
	if binary == "" {
		binary = "codex"
	}
	cmd := exec.CommandContext(ctx, binary, c.args(req, lastMessagePath, workDir)...)
	// An empty scratch dir keeps the agent from reading project files or a
	// project AGENTS.md from wherever researchguy was launched.
	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(codexPrompt(req))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("codex stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting codex CLI: %w", err)
	}
	// Events are read as they arrive, so each web search is captured to
	// the run before the agent goes on, not when it exits.
	parser := codexParser{capture: req.Capture}
	readErr := parser.read(stdout)
	runErr := cmd.Wait()
	run := parser.run
	c.setRun(run.evidence, run.metadataJSON(c))
	if runErr == nil && readErr != nil {
		runErr = fmt.Errorf("reading codex output: %w", readErr)
	}

	if run.failure != "" {
		return "", fmt.Errorf("codex run failed: %s", run.failure)
	}
	if runErr != nil {
		return "", fmt.Errorf("codex CLI failed: %w\nstderr: %s", runErr, tailString(stderr.String(), maxCodexStderrChars))
	}

	text := ""
	if b, err := os.ReadFile(lastMessagePath); err == nil {
		text = strings.TrimSpace(string(b))
	}
	if text == "" {
		text = strings.TrimSpace(run.lastMessage)
	}
	if text == "" {
		return "", fmt.Errorf("codex returned no final message")
	}
	return text, nil
}

func (c *Codex) args(req Request, lastMessagePath, workDir string) []string {
	args := []string{
		"exec",
		"--json",
		"--ephemeral",
		"--skip-git-repo-check",
		"--color", "never",
		"--sandbox", "read-only",
		"--cd", workDir,
		"--output-last-message", lastMessagePath,
	}
	if c.IgnoreUserConfig {
		args = append(args, "--ignore-user-config")
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.ReasoningEffort != "" {
		args = append(args, "-c", "model_reasoning_effort="+tomlString(c.ReasoningEffort))
	}
	webSearch := "disabled"
	if wantsWebTools(req) {
		webSearch = "live"
	}
	args = append(args, "-c", "web_search="+tomlString(webSearch))
	// The prompt goes on stdin so long aggregation prompts never hit argv limits.
	return append(args, "-")
}

// wantsWebTools reports whether the request offers web_search or web_fetch.
// Codex maps them onto its built-in web search, which also opens pages;
// Goose gets them from the researchguy web extension.
func wantsWebTools(req Request) bool {
	for _, t := range req.Tools {
		if t.Name == "web_search" || t.Name == "web_fetch" {
			return true
		}
	}
	return false
}

// codexPrompt folds the system prompt into the user prompt, since codex exec
// has no system prompt flag.
func codexPrompt(req Request) string {
	if strings.TrimSpace(req.SystemPrompt) == "" {
		return req.UserPrompt
	}
	return "<instructions>\n" + strings.TrimSpace(req.SystemPrompt) + "\n</instructions>\n\n" + req.UserPrompt
}

func tomlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func tailString(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return "..." + s[len(s)-max:]
}

func (c *Codex) setRun(evidence []EvidenceRecord, metadata string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastEvidence = evidence
	c.lastMetadata = metadata
}

// --- codex exec --json event stream ---

type codexEvent struct {
	Type    string          `json:"type"`
	Message string          `json:"message"`
	Error   *codexError     `json:"error"`
	Item    *codexItem      `json:"item"`
	Usage   json.RawMessage `json:"usage"`
}

type codexError struct {
	Message string `json:"message"`
}

type codexItem struct {
	Type    string              `json:"type"`
	Text    string              `json:"text"`
	Query   string              `json:"query"`
	Action  codexSearchAction   `json:"action"`
	Results []codexSearchResult `json:"results"`
}

type codexSearchAction struct {
	Type  string `json:"type"`
	Query string `json:"query"`
	URL   string `json:"url"`
}

type codexSearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	RefID   string `json:"ref_id"`
}

type codexRun struct {
	lastMessage string
	evidence    []EvidenceRecord
	webSearches int
	usage       json.RawMessage
	failure     string
}

// codexParser reads the codex exec --json event stream. capture, when set,
// gets each web search result as its event is read.
type codexParser struct {
	run     codexRun
	capture CaptureFunc
}

func parseCodexEvents(stdout []byte) codexRun {
	var p codexParser
	_ = p.read(bytes.NewReader(stdout))
	return p.run
}

// read feeds every line of r to the parser. It reads to EOF whatever the
// lines hold, so codex never blocks on a full pipe.
func (p *codexParser) read(r io.Reader) error {
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

func (p *codexParser) feed(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var ev codexEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}
	run := &p.run
	switch ev.Type {
	case "item.completed":
		if ev.Item == nil {
			return
		}
		switch ev.Item.Type {
		case "agent_message":
			run.lastMessage = ev.Item.Text
		case "web_search":
			run.webSearches++
			if rec, ok := codexSearchEvidence(ev.Item); ok {
				if p.capture != nil {
					rec.ID = p.capture(rec)
				}
				run.evidence = append(run.evidence, rec)
			}
		}
	case "turn.completed":
		run.usage = ev.Usage
	case "turn.failed":
		if ev.Error != nil && ev.Error.Message != "" {
			run.failure = ev.Error.Message
		}
	case "error":
		if run.failure == "" {
			run.failure = ev.Message
		}
	}
}

// codexSearchEvidence turns a web_search item into evidence. Codex reports
// a page the model opened as a web_search item whose action has no URL
// (type "other"); the page is the result, with a ref_id like "turn1view0"
// where a search result's is "turn0search0".
func codexSearchEvidence(item *codexItem) (EvidenceRecord, bool) {
	if len(item.Results) == 0 {
		return EvidenceRecord{}, false
	}
	query := item.Query
	if query == "" {
		query = item.Action.Query
	}
	call := store.Call{Tool: "web_search", Action: codexAction(item.Action.Type, query), Query: query, URL: item.Action.URL}
	rank := 0
	var b strings.Builder
	for _, r := range item.Results {
		cr := store.CaptureResult{Title: r.Title, URL: r.URL, Snippet: r.Snippet, RefID: r.RefID}
		if codexOpened(r.RefID, call.Action) {
			cr.Opened = true
		} else {
			rank++
			cr.Rank = rank
		}
		call.Results = append(call.Results, cr)
		fmt.Fprintf(&b, "%s\n%s\n%s\n\n", r.Title, r.URL, r.Snippet)
	}
	if call.URL == "" && call.Action != "search" {
		call.URL = item.Results[0].URL
	}
	label := "web_search: " + query
	if call.Action != "search" {
		label = fmt.Sprintf("web_search %s: %s", call.Action, strings.TrimSpace(call.URL+" "+query))
	}
	return EvidenceRecord{Label: label, Content: strings.TrimSpace(b.String()), Call: call}, true
}

// codexAction names what a web_search item did. "other" is how codex
// reports opening a page.
func codexAction(actionType, query string) string {
	switch actionType {
	case "search":
		return "search"
	case "", "other", "open_page":
		if actionType == "" && query != "" {
			return "search"
		}
		return "open"
	default:
		return actionType // find_in_page and anything newer, as codex names it
	}
}

// codexOpened reports whether a result is a page the model read rather than
// a search hit, from its ref_id when codex gives one.
func codexOpened(refID, action string) bool {
	switch {
	case strings.Contains(refID, "view"):
		return true
	case strings.Contains(refID, "search"):
		return false
	default:
		return action != "search"
	}
}

func (r codexRun) metadataJSON(c *Codex) string {
	meta := struct {
		Backend         string          `json:"backend"`
		Model           string          `json:"model,omitempty"`
		ReasoningEffort string          `json:"reasoning_effort,omitempty"`
		WebSearches     int             `json:"web_searches"`
		EvidenceItems   int             `json:"evidence_items"`
		Usage           json.RawMessage `json:"usage,omitempty"`
		Failure         string          `json:"failure,omitempty"`
	}{
		Backend:         "codex",
		Model:           c.Model,
		ReasoningEffort: c.ReasoningEffort,
		WebSearches:     r.webSearches,
		EvidenceItems:   len(r.evidence),
		Usage:           r.usage,
		Failure:         truncateForMetadata(r.failure, 2000),
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(b)
}
