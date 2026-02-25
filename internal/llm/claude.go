package llm

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Claude shells out to the Claude CLI for completions.
type Claude struct {
	Binary    string
	Model     string
	MaxTokens int
}

func (c *Claude) Name() string {
	return "claude"
}

func (c *Claude) Complete(ctx context.Context, req Request) (string, error) {
	prompt := req.UserPrompt
	if req.SystemPrompt != "" {
		prompt = req.SystemPrompt + "\n\n" + req.UserPrompt
	}

	args := []string{
		"-p", prompt,
		"--model", c.Model,
		"--output-format", "text",
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = c.MaxTokens
	}
	if maxTokens > 0 {
		args = append(args, "--max-tokens", strconv.Itoa(maxTokens))
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
