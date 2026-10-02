package llm

import (
	"bytes"
	"context"
	"encoding/json"
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
	// IgnoreUserConfig keeps the user's CLAUDE.md files, memory, hooks and
	// MCP servers out of the call (claude.ignore_user_config): --safe-mode
	// and --strict-mcp-config, or with Request.MCP no setting sources and
	// --strict-mcp-config, because --safe-mode also turns off the servers
	// --mcp-config names.
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

	out, err := c.run(ctx, dir, req)
	if err != nil && c.IgnoreUserConfig && len(req.MCP) > 0 && strings.Contains(err.Error(), notLoggedIn) {
		// Without setting sources the CLI doesn't read the env block in
		// settings.json, so a token kept only there doesn't reach it.
		// --safe-mode still reads it, but can't run MCP servers.
		fmt.Fprintf(os.Stderr, "Warning: claude isn't logged in without user settings (is CLAUDE_CODE_OAUTH_TOKEN only in settings.json?); retrying without MCP tools\n")
		req.MCP = nil
		return c.run(ctx, dir, req)
	}
	return out, err
}

// notLoggedIn is what the CLI prints when it finds no credentials.
const notLoggedIn = "Not logged in"

func (c *Claude) run(ctx context.Context, dir string, req Request) (string, error) {
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
		// API errors such as "Prompt is too long" come on stdout, with an
		// empty stderr.
		out := strings.TrimSpace(stdout.String())
		if len(out) > 2000 {
			out = out[:2000] + "..."
		}
		return "", fmt.Errorf("claude CLI failed: %w\nstdout: %s\nstderr: %s", err, out, stderr.String())
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

	if len(req.MCP) > 0 {
		path, err := writeMCPConfig(dir, req.MCP)
		if err != nil {
			return nil, err
		}
		args = append(args, "--mcp-config", path)
	}
	if c.IgnoreUserConfig {
		if len(req.MCP) > 0 {
			// No setting sources also keeps CLAUDE.md and memory out, and
			// tools not allowed below are denied (Claude Code 2.1.282).
			args = append(args, "--setting-sources", "", "--strict-mcp-config")
		} else {
			args = append(args, "--safe-mode", "--strict-mcp-config")
		}
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
	// mcp__<server> allows every tool the server serves.
	allowed := claudeTools
	for _, m := range req.MCP {
		allowed = append(allowed, "mcp__"+m.Name)
	}
	if len(allowed) > 0 {
		for _, ct := range allowed {
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

// writeMCPConfig writes the servers as an --mcp-config file in dir.
func writeMCPConfig(dir string, servers []MCPServer) (string, error) {
	type server struct {
		Command string            `json:"command"`
		Args    []string          `json:"args,omitempty"`
		Env     map[string]string `json:"env,omitempty"`
	}
	conf := struct {
		MCPServers map[string]server `json:"mcpServers"`
	}{MCPServers: map[string]server{}}
	for _, m := range servers {
		conf.MCPServers[m.Name] = server{Command: m.Command, Args: m.Args, Env: m.Env}
	}
	b, err := json.Marshal(conf)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", fmt.Errorf("writing claude MCP config: %w", err)
	}
	return path, nil
}
