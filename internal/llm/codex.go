package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
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
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	run := parseCodexEvents(stdout.Bytes())
	c.setRun(run.evidence, run.metadataJSON(c))

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
	if codexWantsWeb(req) {
		webSearch = "live"
	}
	args = append(args, "-c", "web_search="+tomlString(webSearch))
	// The prompt goes on stdin so long aggregation prompts never hit argv limits.
	return append(args, "-")
}

// codexWantsWeb maps our web_search/web_fetch tools onto Codex's built-in
// web search, which also opens pages.
func codexWantsWeb(req Request) bool {
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
}

type codexRun struct {
	lastMessage string
	evidence    []EvidenceRecord
	webSearches int
	usage       json.RawMessage
	failure     string
}

func parseCodexEvents(stdout []byte) codexRun {
	var run codexRun
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 0, 64*1024), 32*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev codexEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "item.completed":
			if ev.Item == nil {
				continue
			}
			switch ev.Item.Type {
			case "agent_message":
				run.lastMessage = ev.Item.Text
			case "web_search":
				run.webSearches++
				if rec, ok := codexSearchEvidence(ev.Item); ok {
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
	return run
}

func codexSearchEvidence(item *codexItem) (EvidenceRecord, bool) {
	if len(item.Results) == 0 {
		return EvidenceRecord{}, false
	}
	query := item.Query
	if query == "" {
		query = item.Action.Query
	}
	label := "web_search: " + query
	if item.Action.Type != "" && item.Action.Type != "search" {
		label = fmt.Sprintf("web_search %s: %s", item.Action.Type, strings.TrimSpace(item.Action.URL+" "+query))
	}
	var b strings.Builder
	for _, r := range item.Results {
		fmt.Fprintf(&b, "%s\n%s\n%s\n\n", r.Title, r.URL, r.Snippet)
	}
	return EvidenceRecord{Label: label, Content: strings.TrimSpace(b.String())}, true
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
