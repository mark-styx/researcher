package llm

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Claude shells out to the Claude CLI for completions.
type Claude struct {
	Binary       string
	Model        string
	MaxTokens    int     // Deprecated: Claude CLI no longer supports --max-tokens.
	MaxBudgetUSD float64 // Optional max spend per call (--max-budget-usd).
}

func (c *Claude) Name() string {
	return "claude"
}

// toolNameMap maps our tool names to Claude CLI tool names.
var toolNameMap = map[string]string{
	"web_search": "WebSearch",
	"web_fetch":  "WebFetch",
}

func (c *Claude) Complete(ctx context.Context, req Request) (string, error) {
	args := []string{
		"-p", req.UserPrompt,
		"--model", c.Model,
		"--output-format", "text",
	}

	// Use --system-prompt for proper system prompt handling
	if req.SystemPrompt != "" {
		args = append(args, "--system-prompt", req.SystemPrompt)
	}

	// Apply budget limit if configured
	if c.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", c.MaxBudgetUSD))
	}

	// Enable tools if requested
	if len(req.Tools) > 0 {
		var claudeTools []string
		for _, t := range req.Tools {
			if mapped, ok := toolNameMap[t.Name]; ok {
				claudeTools = append(claudeTools, mapped)
			}
		}
		if len(claudeTools) > 0 {
			for _, ct := range claudeTools {
				args = append(args, "--allowedTools", ct)
			}
			args = append(args, "--max-turns", "25")
		}
	}

	cmd := exec.CommandContext(ctx, c.Binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claude CLI failed: %w\nstderr: %s", err, stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}
