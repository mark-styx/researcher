package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/marklubin/researcher/internal/tools"
)

// Ollama uses the Ollama HTTP API for completions.
type Ollama struct {
	Host          string
	Model         string
	FallbackModel string
	MaxIterations int
	Executor      *tools.Executor
}

func (o *Ollama) Name() string {
	return "ollama"
}

// --- Ollama /api/chat types ---

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Tools    json.RawMessage `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaFunction `json:"function"`
}

type ollamaFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ollamaChatResponse struct {
	Message ollamaMessage `json:"message"`
	Done    bool          `json:"done"`
	Error   string        `json:"error,omitempty"`
}

func (o *Ollama) Complete(ctx context.Context, req Request) (string, error) {
	resp, err := o.doChat(ctx, o.Model, req)
	if err != nil && o.FallbackModel != "" && o.FallbackModel != o.Model {
		fmt.Printf("Primary model %q failed, trying fallback %q...\n", o.Model, o.FallbackModel)
		resp, err = o.doChat(ctx, o.FallbackModel, req)
	}
	return resp, err
}

func (o *Ollama) doChat(ctx context.Context, model string, req Request) (string, error) {
	// Build initial messages
	var messages []ollamaMessage
	if req.SystemPrompt != "" {
		messages = append(messages, ollamaMessage{Role: "system", Content: req.SystemPrompt})
	}
	messages = append(messages, ollamaMessage{Role: "user", Content: req.UserPrompt})

	// Convert tools to Ollama format
	var toolsJSON json.RawMessage
	if len(req.Tools) > 0 {
		ollamaTools := convertToolsToOllama(req.Tools)
		data, err := json.Marshal(ollamaTools)
		if err != nil {
			return "", fmt.Errorf("marshaling tools: %w", err)
		}
		toolsJSON = data
	}

	maxIter := o.MaxIterations
	if maxIter <= 0 {
		maxIter = 20
	}

	for iter := 0; iter < maxIter; iter++ {
		chatReq := ollamaChatRequest{
			Model:    model,
			Messages: messages,
			Tools:    toolsJSON,
			Stream:   false, // streaming + tools is unreliable
		}

		respMsg, err := o.sendChat(ctx, chatReq)
		if err != nil {
			return "", err
		}

		// No tool calls — return the content
		if len(respMsg.ToolCalls) == 0 {
			return strings.TrimSpace(respMsg.Content), nil
		}

		// Append assistant message with tool calls
		messages = append(messages, *respMsg)

		// Execute each tool call and append results
		for _, tc := range respMsg.ToolCalls {
			args := tools.ParseToolArguments(tc.Function.Arguments)
			call := tools.ToolCall{
				Name:      tc.Function.Name,
				Arguments: args,
			}

			fmt.Printf("[tool] %s(%v)\n", call.Name, formatArgs(args))
			result := o.Executor.Execute(ctx, call)

			messages = append(messages, ollamaMessage{
				Role:    "tool",
				Content: result.Content,
			})
		}
	}

	// Hit max iterations — return whatever content we have from the last assistant message
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && messages[i].Content != "" {
			return strings.TrimSpace(messages[i].Content), nil
		}
	}

	return "", fmt.Errorf("max tool iterations (%d) exceeded with no final response", maxIter)
}

func (o *Ollama) sendChat(ctx context.Context, req ollamaChatRequest) (*ollamaMessage, error) {
	jsonBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	url := strings.TrimRight(o.Host, "/") + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("ollama returned %d: %s", httpResp.StatusCode, string(respBody))
	}

	var resp ollamaChatResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("ollama error: %s", resp.Error)
	}

	return &resp.Message, nil
}

// convertToolsToOllama converts our tool definitions to Ollama's expected format.
func convertToolsToOllama(tt []tools.Tool) []map[string]interface{} {
	var result []map[string]interface{}
	for _, t := range tt {
		result = append(result, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		})
	}
	return result
}

func formatArgs(args map[string]interface{}) string {
	if args == nil {
		return ""
	}
	var parts []string
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, ", ")
}
