package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewExecutor(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{5, 5},
		{0, 10},
		{-1, 10},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("input=%d", tc.input), func(t *testing.T) {
			e := NewExecutor(tc.input)
			if e.MaxResults != tc.want {
				t.Errorf("NewExecutor(%d).MaxResults = %d, want %d", tc.input, e.MaxResults, tc.want)
			}
		})
	}
}

func TestParseToolArguments(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantNil bool
		wantKey string
		wantVal string
	}{
		{
			name:    "raw JSON object",
			raw:     `{"query":"hello"}`,
			wantKey: "query",
			wantVal: "hello",
		},
		{
			name:    "string-encoded JSON",
			raw:     `"{\"query\":\"world\"}"`,
			wantKey: "query",
			wantVal: "world",
		},
		{
			name:    "invalid JSON",
			raw:     `not json at all`,
			wantNil: true,
		},
		{
			name:    "null",
			raw:     `null`,
			wantNil: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := ParseToolArguments(json.RawMessage(tc.raw))
			if tc.wantNil {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
				return
			}
			if result == nil {
				t.Fatal("expected non-nil result")
			}
			val, ok := result[tc.wantKey].(string)
			if !ok || val != tc.wantVal {
				t.Errorf("result[%q] = %v, want %q", tc.wantKey, result[tc.wantKey], tc.wantVal)
			}
		})
	}
}

func TestExecute_UnknownTool(t *testing.T) {
	e := NewExecutor(10)
	result := e.Execute(context.Background(), ToolCall{Name: "bogus"})
	if !result.IsError {
		t.Error("expected IsError=true for unknown tool")
	}
	if result.Content == "" {
		t.Error("expected non-empty error content")
	}
}

func TestExecute_WebSearch_MissingQuery(t *testing.T) {
	e := NewExecutor(10)
	result := e.Execute(context.Background(), ToolCall{
		Name:      "web_search",
		Arguments: map[string]interface{}{},
	})
	if !result.IsError {
		t.Error("expected IsError=true for missing query")
	}
}

func TestExecute_WebFetch_MissingURL(t *testing.T) {
	e := NewExecutor(10)
	result := e.Execute(context.Background(), ToolCall{
		Name:      "web_fetch",
		Arguments: map[string]interface{}{},
	})
	if !result.IsError {
		t.Error("expected IsError=true for missing url")
	}
}

func TestExecute_WebFetch_Success(t *testing.T) {
	t.Setenv("RESEARCHGUY_ALLOW_PRIVATE_URLS", "true")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "hello from test server")
	}))
	defer srv.Close()

	e := NewExecutor(10)
	result := e.Execute(context.Background(), ToolCall{
		Name:      "web_fetch",
		Arguments: map[string]interface{}{"url": srv.URL},
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Content != "hello from test server" {
		t.Errorf("content = %q, want %q", result.Content, "hello from test server")
	}
}
