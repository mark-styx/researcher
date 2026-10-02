package llm

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Claude shells out to the Claude CLI for completions.
type Claude struct {
	Binary       string
	Model        string
	MaxTokens    int     // Deprecated: Claude CLI no longer supports --max-tokens.
	MaxBudgetUSD float64 // Optional max spend per call (--max-budget-usd).
	MaxTurns     int     // Max agentic turns when tools are enabled (default 50).
	// IgnoreUserConfig adds --safe-mode and --strict-mcp-config so the
	// user's CLAUDE.md files, memory, hooks and MCP servers stay out of the
	// call (claude.ignore_user_config).
	IgnoreUserConfig bool
}

func (c *Claude) Name() string {
	return "claude"
}

// toolNameMap maps our tool names to Claude CLI tool names.
var toolNameMap = map[string]string{
	"web_search": "WebSearch",
	"web_fetch":  "WebFetch",
}

// Complete runs one `claude -p` call. The prompt goes on stdin and the
// system prompt in a file, because argv is capped at ARG_MAX (1 MiB on
// macOS) and aggregation prompts are larger than that. The call runs in an
// empty temporary directory with only the requested tools available:
// researchguy uses Claude as a completion backend, so it never needs, and
// must not act on, the caller's working directory.
func (c *Claude) Complete(ctx context.Context, req Request) (string, error) {
	dir, err := os.MkdirTemp("", "researchguy-claude-")
	if err != nil {
		return "", fmt.Errorf("creating claude work dir: %w", err)
	}
	defer os.RemoveAll(dir)

	args, err := c.args(dir, req)
	if err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, c.Binary, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(req.UserPrompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claude CLI failed: %w\nstderr: %s", err, stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

func (c *Claude) args(dir string, req Request) ([]string, error) {
	args := []string{
		"-p",
		"--model", c.Model,
		"--output-format", "text",
		"--no-session-persistence",
	}

	if req.SystemPrompt != "" {
		path := filepath.Join(dir, "system-prompt.md")
		if err := os.WriteFile(path, []byte(req.SystemPrompt), 0o600); err != nil {
			return nil, fmt.Errorf("writing claude system prompt: %w", err)
		}
		args = append(args, "--system-prompt-file", path)
	}

	if c.IgnoreUserConfig {
		args = append(args, "--safe-mode", "--strict-mcp-config")
	}

	if c.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", c.MaxBudgetUSD))
	}

	// --tools sets which built-in tools exist at all; "" means none. Without
	// it a no-tools call still had Bash, Edit and the rest under the user's
	// permission mode.
	var claudeTools []string
	for _, t := range req.Tools {
		if mapped, ok := toolNameMap[t.Name]; ok {
			claudeTools = append(claudeTools, mapped)
		}
	}
	args = append(args, "--tools", strings.Join(claudeTools, ","))
	if len(claudeTools) > 0 {
		for _, ct := range claudeTools {
			args = append(args, "--allowedTools", ct)
		}
		maxTurns := c.MaxTurns
		if maxTurns <= 0 {
			maxTurns = 50
		}
		args = append(args, "--max-turns", fmt.Sprintf("%d", maxTurns))
	}
	return args, nil
}
