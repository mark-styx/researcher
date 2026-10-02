package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// Executor dispatches tool calls to their implementations.
type Executor struct {
	MaxResults int
}

// NewExecutor creates an Executor with the given max search results.
func NewExecutor(maxResults int) *Executor {
	if maxResults <= 0 {
		maxResults = 10
	}
	return &Executor{MaxResults: maxResults}
}

// Execute runs a tool call and returns the result.
func (e *Executor) Execute(ctx context.Context, call ToolCall) ToolResult {
	switch call.Name {
	case "web_search":
		return e.execWebSearch(ctx, call)
	case "web_fetch":
		return e.execWebFetch(ctx, call)
	default:
		return ToolResult{
			Name:    call.Name,
			Content: fmt.Sprintf("unknown tool: %s", call.Name),
			IsError: true,
		}
	}
}

func (e *Executor) execWebSearch(ctx context.Context, call ToolCall) ToolResult {
	query, _ := call.Arguments["query"].(string)
	if query == "" {
		return ToolResult{Name: call.Name, Content: "missing required argument: query", IsError: true}
	}

	results, err := WebSearch(ctx, query, e.MaxResults)
	if err != nil {
		return ToolResult{Name: call.Name, Content: fmt.Sprintf("search failed: %v", err), IsError: true}
	}

	return ToolResult{Name: call.Name, Content: FormatSearchResults(results), Results: results}
}

func (e *Executor) execWebFetch(ctx context.Context, call ToolCall) ToolResult {
	url, _ := call.Arguments["url"].(string)
	if url == "" {
		return ToolResult{Name: call.Name, Content: "missing required argument: url", IsError: true}
	}

	content, err := WebFetch(ctx, url)
	if err != nil {
		return ToolResult{Name: call.Name, Content: fmt.Sprintf("fetch failed: %v", err), IsError: true}
	}

	return ToolResult{Name: call.Name, Content: content}
}

// ParseToolArguments handles both raw JSON objects and string-encoded JSON.
func ParseToolArguments(raw json.RawMessage) map[string]interface{} {
	// Try as object first
	var args map[string]interface{}
	if err := json.Unmarshal(raw, &args); err == nil {
		return args
	}

	// Try as string-encoded JSON
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if err2 := json.Unmarshal([]byte(s), &args); err2 == nil {
			return args
		}
	}

	return nil
}
