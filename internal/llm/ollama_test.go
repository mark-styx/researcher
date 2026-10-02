package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marklubin/researchguy/internal/tools"
)

func TestOllama_Name(t *testing.T) {
	o := &Ollama{}
	if got := o.Name(); got != "ollama" {
		t.Errorf("Name() = %q, want %q", got, "ollama")
	}
}

func TestOllama_Complete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaChatRequest
		json.NewDecoder(r.Body).Decode(&req)

		resp := ollamaChatResponse{
			Message: ollamaMessage{Role: "assistant", Content: "completed response"},
			Done:    true,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	o := &Ollama{
		Host:     srv.URL,
		Model:    "primary",
		Executor: tools.NewExecutor(10),
	}

	got, err := o.Complete(context.Background(), Request{UserPrompt: "hello"})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got != "completed response" {
		t.Errorf("Complete() = %q, want %q", got, "completed response")
	}
}

func TestOllama_Complete_Fallback(t *testing.T) {
	var requestedModels []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		requestedModels = append(requestedModels, req.Model)

		if req.Model == "primary" {
			// Return error for primary model
			resp := ollamaChatResponse{
				Error: "model not found",
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		// Fallback succeeds
		resp := ollamaChatResponse{
			Message: ollamaMessage{Role: "assistant", Content: "fallback response"},
			Done:    true,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	o := &Ollama{
		Host:          srv.URL,
		Model:         "primary",
		FallbackModel: "fallback",
		Executor:      tools.NewExecutor(10),
	}

	got, err := o.Complete(context.Background(), Request{UserPrompt: "hello"})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got != "fallback response" {
		t.Errorf("Complete() = %q, want %q", got, "fallback response")
	}
	if len(requestedModels) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(requestedModels))
	}
	if requestedModels[0] != "primary" {
		t.Errorf("first request model = %q, want %q", requestedModels[0], "primary")
	}
	if requestedModels[1] != "fallback" {
		t.Errorf("second request model = %q, want %q", requestedModels[1], "fallback")
	}
}

func TestDoChat_MaxIterationsExceeded(t *testing.T) {
	// Server always returns tool calls, never a final answer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		argsJSON, _ := json.Marshal(map[string]string{"url": "http://example.com"})
		resp := ollamaChatResponse{
			Message: ollamaMessage{
				Role: "assistant",
				ToolCalls: []ollamaToolCall{
					{Function: ollamaFunction{Name: "web_fetch", Arguments: argsJSON}},
				},
			},
			Done: false,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	o := &Ollama{
		Host:          srv.URL,
		MaxIterations: 2,
		Executor:      tools.NewExecutor(10),
	}

	_, err := o.doChat(context.Background(), "test", Request{
		UserPrompt: "hello",
		Tools:      tools.DefaultTools(),
	})
	if err == nil {
		t.Fatal("expected error for max iterations exceeded")
	}
	if !strings.Contains(err.Error(), "max tool iterations") {
		t.Errorf("error = %q, want to contain 'max tool iterations'", err.Error())
	}
}

func TestDoChat_WithSystemPrompt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaChatRequest
		json.NewDecoder(r.Body).Decode(&req)

		// Verify system prompt is included
		if len(req.Messages) < 2 || req.Messages[0].Role != "system" {
			http.Error(w, "missing system prompt", 400)
			return
		}

		resp := ollamaChatResponse{
			Message: ollamaMessage{Role: "assistant", Content: "sys response"},
			Done:    true,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	o := &Ollama{Host: srv.URL, Executor: tools.NewExecutor(10)}
	got, err := o.doChat(context.Background(), "test", Request{
		SystemPrompt: "You are a helper",
		UserPrompt:   "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "sys response" {
		t.Errorf("got %q, want %q", got, "sys response")
	}
}

func TestSendChat_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	o := &Ollama{Host: srv.URL}
	_, err := o.sendChat(context.Background(), ollamaChatRequest{
		Model:    "test",
		Messages: []ollamaMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

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
	evidence := o.Evidence()
	if len(evidence) != 1 {
		t.Fatalf("Evidence() returned %d entries, want 1", len(evidence))
	}
	if !strings.Contains(evidence[0].Label, "web_fetch") || !strings.Contains(evidence[0].Label, contentSrv.URL) {
		t.Errorf("evidence label = %q, want tool name and URL", evidence[0].Label)
	}
	if evidence[0].Content != "fetched content" {
		t.Errorf("evidence content = %q", evidence[0].Content)
	}
}

func TestOllama_RequestResourceControls(t *testing.T) {
	o := &Ollama{
		NumCtx:     32768,
		NumPredict: 4096,
		KeepAlive:  "5m",
	}
	options := o.requestOptions(Request{MaxTokens: 16000})
	if options.NumCtx != 32768 {
		t.Errorf("num_ctx = %d, want 32768", options.NumCtx)
	}
	if options.NumPredict != 4096 {
		t.Errorf("num_predict = %d, want configured cap 4096", options.NumPredict)
	}
	if o.chatKeepAlive() != "5m" {
		t.Errorf("keep_alive = %q, want 5m", o.chatKeepAlive())
	}
	body, err := json.Marshal(ollamaChatRequest{Options: options, KeepAlive: o.chatKeepAlive()})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"num_ctx":32768`, `"num_predict":4096`, `"keep_alive":"5m"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("request JSON %s missing %s", body, want)
		}
	}
}

func TestOllama_RequestMaxTokensBelowConfiguredCap(t *testing.T) {
	o := &Ollama{NumPredict: 4096}
	options := o.requestOptions(Request{MaxTokens: 200})
	if options.NumPredict != 200 {
		t.Errorf("num_predict = %d, want request limit 200", options.NumPredict)
	}
}

func TestOllama_ZeroKeepAliveUnloadsAfterCompletion(t *testing.T) {
	for _, value := range []string{"0", "0s", "0m"} {
		o := &Ollama{KeepAlive: value}
		if !keepAliveUnloads(value) {
			t.Errorf("keepAliveUnloads(%q) = false", value)
		}
		if got := o.chatKeepAlive(); got != "" {
			t.Errorf("chat keep_alive for %q = %q, want omitted so tool loops do not reload", value, got)
		}
	}
	if keepAliveUnloads("5m") {
		t.Error("5m should retain the model")
	}
	body, err := marshalUnloadRequest("test")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"model":"test","keep_alive":0}` {
		t.Errorf("unload JSON = %s", body)
	}
}

func TestOllama_MetadataIncludesAPITelemetry(t *testing.T) {
	runMeta := ollamaRunMetadata{Model: "test"}
	runMeta.add(&ollamaChatResponse{
		Model:              "test",
		TotalDuration:      12,
		LoadDuration:       3,
		PromptEvalCount:    40,
		PromptEvalDuration: 4,
		EvalCount:          20,
		EvalDuration:       5,
	})
	o := &Ollama{}
	o.storeRun(ollamaRun{metadata: runMeta})
	metadata := o.Metadata()
	for _, want := range []string{`"model":"test"`, `"prompt_eval_count":40`, `"eval_count":20`, `"total_duration_ns":12`} {
		if !strings.Contains(metadata, want) {
			t.Errorf("metadata %q missing %q", metadata, want)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type fakeToolExecutor struct {
	result tools.ToolResult
}

func (f fakeToolExecutor) Execute(context.Context, tools.ToolCall) tools.ToolResult {
	return f.result
}

func jsonHTTPResponse(status int, value any) *http.Response {
	body, _ := json.Marshal(value)
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
}

func TestOllama_CompleteSendsBoundsAndUnloads(t *testing.T) {
	var chatReq ollamaChatRequest
	var unloadBody map[string]any
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/chat":
			if err := json.NewDecoder(req.Body).Decode(&chatReq); err != nil {
				t.Fatalf("decode chat request: %v", err)
			}
			return jsonHTTPResponse(http.StatusOK, ollamaChatResponse{
				Model:           "bounded-model",
				Message:         ollamaMessage{Role: "assistant", Content: "done"},
				Done:            true,
				TotalDuration:   100,
				PromptEvalCount: 30,
				EvalCount:       10,
			}), nil
		case "/api/generate":
			if err := json.NewDecoder(req.Body).Decode(&unloadBody); err != nil {
				t.Fatalf("decode unload request: %v", err)
			}
			return jsonHTTPResponse(http.StatusOK, map[string]any{"done": true}), nil
		default:
			return jsonHTTPResponse(http.StatusNotFound, map[string]any{"error": "not found"}), nil
		}
	})}
	o := &Ollama{
		Host:       "http://ollama.test",
		Model:      "bounded-model",
		NumCtx:     32768,
		NumPredict: 4096,
		KeepAlive:  "0s",
		HTTPClient: client,
	}
	got, err := o.Complete(context.Background(), Request{UserPrompt: "test", MaxTokens: 16000})
	if err != nil {
		t.Fatal(err)
	}
	if got != "done" {
		t.Errorf("Complete() = %q", got)
	}
	if chatReq.Options.NumCtx != 32768 || chatReq.Options.NumPredict != 4096 || chatReq.KeepAlive != "" {
		t.Errorf("chat request = %+v", chatReq)
	}
	if unloadBody["model"] != "bounded-model" || unloadBody["keep_alive"] != float64(0) {
		t.Errorf("unload body = %#v", unloadBody)
	}
	if !strings.Contains(o.Metadata(), `"prompt_eval_count":30`) || !strings.Contains(o.Metadata(), `"eval_count":10`) {
		t.Errorf("metadata = %s", o.Metadata())
	}
}

func TestOllama_CompleteCapturesSuccessfulToolEvidence(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return jsonHTTPResponse(http.StatusNotFound, nil), nil
		}
		if calls.Add(1) == 1 {
			args, _ := json.Marshal(map[string]string{"url": "https://example.com/source"})
			return jsonHTTPResponse(http.StatusOK, ollamaChatResponse{
				Message: ollamaMessage{Role: "assistant", ToolCalls: []ollamaToolCall{{
					Function: ollamaFunction{Name: "web_fetch", Arguments: args},
				}}},
			}), nil
		}
		return jsonHTTPResponse(http.StatusOK, ollamaChatResponse{
			Message: ollamaMessage{Role: "assistant", Content: "answer"},
			Done:    true,
		}), nil
	})}
	o := &Ollama{
		Host:          "http://ollama.test",
		Model:         "tool-model",
		KeepAlive:     "5m",
		MaxIterations: 3,
		HTTPClient:    client,
		Executor: fakeToolExecutor{result: tools.ToolResult{
			Name:    "web_fetch",
			Content: "raw fetched source",
		}},
	}
	if _, err := o.Complete(context.Background(), Request{UserPrompt: "research", Tools: tools.DefaultTools()}); err != nil {
		t.Fatal(err)
	}
	evidence := o.Evidence()
	if len(evidence) != 1 || evidence[0].Content != "raw fetched source" || !strings.Contains(evidence[0].Label, "https://example.com/source") {
		t.Errorf("Evidence() = %+v", evidence)
	}
}

type toolExecutorFunc func(tools.ToolCall) tools.ToolResult

func (f toolExecutorFunc) Execute(_ context.Context, call tools.ToolCall) tools.ToolResult {
	return f(call)
}

// Each tool result is handed to Request.Capture in the tool loop, with the
// call's structure, before the next model turn.
func TestOllama_CompleteCapturesEachToolResultAsItArrives(t *testing.T) {
	var calls atomic.Int32
	var capturedBeforeTurn2 int
	var captured []EvidenceRecord
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return jsonHTTPResponse(http.StatusNotFound, nil), nil
		}
		if calls.Add(1) == 1 {
			search, _ := json.Marshal(map[string]string{"query": "go generics"})
			fetch, _ := json.Marshal(map[string]string{"url": "https://go.dev/blog/intro-generics"})
			return jsonHTTPResponse(http.StatusOK, ollamaChatResponse{
				Message: ollamaMessage{Role: "assistant", ToolCalls: []ollamaToolCall{
					{Function: ollamaFunction{Name: "web_search", Arguments: search}},
					{Function: ollamaFunction{Name: "web_fetch", Arguments: fetch}},
					{Function: ollamaFunction{Name: "web_fetch", Arguments: fetch}}, // fails: not captured
				}},
			}), nil
		}
		capturedBeforeTurn2 = len(captured)
		return jsonHTTPResponse(http.StatusOK, ollamaChatResponse{
			Message: ollamaMessage{Role: "assistant", Content: "answer"},
			Done:    true,
		}), nil
	})}
	fetches := 0
	o := &Ollama{
		Host:          "http://ollama.test",
		Model:         "tool-model",
		MaxIterations: 3,
		HTTPClient:    client,
		Executor: toolExecutorFunc(func(call tools.ToolCall) tools.ToolResult {
			if call.Name == "web_search" {
				results := []tools.SearchResult{{Title: "A", URL: "https://a.test/", Snippet: "a"}, {Title: "B", URL: "https://b.test/"}}
				return tools.ToolResult{Name: call.Name, Content: tools.FormatSearchResults(results), Results: results}
			}
			if fetches++; fetches > 1 {
				return tools.ToolResult{Name: call.Name, Content: "fetch failed: 403", IsError: true}
			}
			return tools.ToolResult{Name: call.Name, Content: "page text"}
		}),
	}
	_, err := o.Complete(context.Background(), Request{
		UserPrompt: "research",
		Tools:      tools.DefaultTools(),
		Capture: func(rec EvidenceRecord) string {
			captured = append(captured, rec)
			return fmt.Sprintf("E%d", len(captured))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if capturedBeforeTurn2 != 2 || len(captured) != 2 {
		t.Fatalf("captured %d before the next turn, %d total; want 2 and 2", capturedBeforeTurn2, len(captured))
	}
	search, fetch := captured[0], captured[1]
	if search.Tool != "web_search" || search.Action != "search" || search.Query != "go generics" ||
		len(search.Results) != 2 || search.Results[1].Rank != 2 || search.Results[1].URL != "https://b.test/" {
		t.Errorf("search capture = %+v", search)
	}
	if fetch.Tool != "web_fetch" || fetch.Action != "fetch" || fetch.URL != "https://go.dev/blog/intro-generics" || fetch.Content != "page text" {
		t.Errorf("fetch capture = %+v", fetch)
	}
	ev := o.Evidence()
	if len(ev) != 2 || ev[0].ID != "E1" || ev[1].ID != "E2" {
		t.Errorf("Evidence() = %+v, want the captured IDs", ev)
	}
}
