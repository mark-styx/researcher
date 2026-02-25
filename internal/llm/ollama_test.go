package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marklubin/researcher/internal/tools"
)

func TestConvertToolsToOllama(t *testing.T) {
	tt := tools.DefaultTools()
	result := convertToolsToOllama(tt)

	if len(result) != len(tt) {
		t.Fatalf("expected %d tools, got %d", len(tt), len(result))
	}

	for i, item := range result {
		typ, ok := item["type"].(string)
		if !ok || typ != "function" {
			t.Errorf("tool[%d] type = %v, want 'function'", i, item["type"])
		}

		fn, ok := item["function"].(map[string]interface{})
		if !ok {
			t.Fatalf("tool[%d] function is not a map", i)
		}

		name, _ := fn["name"].(string)
		if name != tt[i].Name {
			t.Errorf("tool[%d] function.name = %q, want %q", i, name, tt[i].Name)
		}

		if _, ok := fn["description"]; !ok {
			t.Errorf("tool[%d] missing function.description", i)
		}
		if _, ok := fn["parameters"]; !ok {
			t.Errorf("tool[%d] missing function.parameters", i)
		}
	}
}

func TestFormatArgs(t *testing.T) {
	t.Run("nil args", func(t *testing.T) {
		got := formatArgs(nil)
		if got != "" {
			t.Errorf("formatArgs(nil) = %q, want empty string", got)
		}
	})

	t.Run("single kv pair", func(t *testing.T) {
		got := formatArgs(map[string]interface{}{"key": "value"})
		if got != "key=value" {
			t.Errorf("formatArgs = %q, want %q", got, "key=value")
		}
	})
}

func TestSendChat(t *testing.T) {
	t.Run("valid response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := ollamaChatResponse{
				Message: ollamaMessage{Role: "assistant", Content: "Hello!"},
				Done:    true,
			}
			json.NewEncoder(w).Encode(resp)
		}))
		defer srv.Close()

		o := &Ollama{Host: srv.URL}
		msg, err := o.sendChat(context.Background(), ollamaChatRequest{
			Model:    "test",
			Messages: []ollamaMessage{{Role: "user", Content: "hi"}},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msg.Content != "Hello!" {
			t.Errorf("content = %q, want %q", msg.Content, "Hello!")
		}
	})

	t.Run("error status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "server error")
		}))
		defer srv.Close()

		o := &Ollama{Host: srv.URL}
		_, err := o.sendChat(context.Background(), ollamaChatRequest{
			Model:    "test",
			Messages: []ollamaMessage{{Role: "user", Content: "hi"}},
		})
		if err == nil {
			t.Fatal("expected error for 500 status")
		}
		if !strings.Contains(err.Error(), "500") {
			t.Errorf("error = %q, expected to contain '500'", err.Error())
		}
	})

	t.Run("ollama error field", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := ollamaChatResponse{
				Error: "model not found",
			}
			json.NewEncoder(w).Encode(resp)
		}))
		defer srv.Close()

		o := &Ollama{Host: srv.URL}
		_, err := o.sendChat(context.Background(), ollamaChatRequest{
			Model:    "test",
			Messages: []ollamaMessage{{Role: "user", Content: "hi"}},
		})
		if err == nil {
			t.Fatal("expected error for ollama error field")
		}
		if !strings.Contains(err.Error(), "model not found") {
			t.Errorf("error = %q, expected to contain 'model not found'", err.Error())
		}
	})
}

func TestDoChat_NoTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ollamaChatResponse{
			Message: ollamaMessage{Role: "assistant", Content: "Simple answer"},
			Done:    true,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	o := &Ollama{
		Host:     srv.URL,
		Executor: tools.NewExecutor(10),
	}
	got, err := o.doChat(context.Background(), "test", Request{
		UserPrompt: "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Simple answer" {
		t.Errorf("got %q, want %q", got, "Simple answer")
	}
}

func TestDoChat_WithToolCalls(t *testing.T) {
	// Set up a web server to be fetched by the tool executor
	contentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "fetched content")
	}))
	defer contentSrv.Close()

	// Track LLM call count to return tool call first, then final answer
	var callCount atomic.Int32

	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n == 1 {
			// First call: return a tool call
			argsJSON, _ := json.Marshal(map[string]string{"url": contentSrv.URL})
			resp := ollamaChatResponse{
				Message: ollamaMessage{
					Role: "assistant",
					ToolCalls: []ollamaToolCall{
						{
							Function: ollamaFunction{
								Name:      "web_fetch",
								Arguments: argsJSON,
							},
						},
					},
				},
				Done: false,
			}
			json.NewEncoder(w).Encode(resp)
		} else {
			// Second call: return final content
			resp := ollamaChatResponse{
				Message: ollamaMessage{Role: "assistant", Content: "Final answer with fetched data"},
				Done:    true,
			}
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer llmSrv.Close()

	o := &Ollama{
		Host:          llmSrv.URL,
		MaxIterations: 5,
		Executor:      tools.NewExecutor(10),
	}

	got, err := o.doChat(context.Background(), "test", Request{
		UserPrompt: "fetch something",
		Tools:      tools.DefaultTools(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Final answer with fetched data" {
		t.Errorf("got %q, want %q", got, "Final answer with fetched data")
	}
	if callCount.Load() != 2 {
		t.Errorf("expected 2 LLM calls, got %d", callCount.Load())
	}
}
