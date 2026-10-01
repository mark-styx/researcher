package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/marklubin/researchguy/internal/tools"
)

// Ollama uses the Ollama HTTP API for completions.
type Ollama struct {
	Host          string
	Model         string
	FallbackModel string
	NumCtx        int
	NumPredict    int
	KeepAlive     string
	MaxIterations int
	Executor      ToolExecutor
	HTTPClient    *http.Client

	runMu        sync.Mutex
	mu           sync.RWMutex
	lastEvidence []EvidenceRecord
	lastMetadata string
	activeModel  string
	loaded       bool
}

// ToolExecutor runs a model-requested tool call.
type ToolExecutor interface {
	Execute(ctx context.Context, call tools.ToolCall) tools.ToolResult
}

func (o *Ollama) Name() string {
	return "ollama"
}

func (o *Ollama) Metadata() string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.lastMetadata
}

func (o *Ollama) Evidence() []EvidenceRecord {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return append([]EvidenceRecord(nil), o.lastEvidence...)
}

// --- Ollama /api/chat types ---

type ollamaChatRequest struct {
	Model     string          `json:"model"`
	Messages  []ollamaMessage `json:"messages"`
	Tools     json.RawMessage `json:"tools,omitempty"`
	Stream    bool            `json:"stream"`
	Options   ollamaOptions   `json:"options,omitempty"`
	KeepAlive string          `json:"keep_alive,omitempty"`
}

type ollamaOptions struct {
	NumCtx     int `json:"num_ctx,omitempty"`
	NumPredict int `json:"num_predict,omitempty"`
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
	Model              string        `json:"model"`
	Message            ollamaMessage `json:"message"`
	Done               bool          `json:"done"`
	Error              string        `json:"error,omitempty"`
	TotalDuration      int64         `json:"total_duration,omitempty"`
	LoadDuration       int64         `json:"load_duration,omitempty"`
	PromptEvalCount    int           `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64         `json:"prompt_eval_duration,omitempty"`
	EvalCount          int           `json:"eval_count,omitempty"`
	EvalDuration       int64         `json:"eval_duration,omitempty"`
}

type ollamaRunMetadata struct {
	Model              string `json:"model"`
	Calls              int    `json:"calls"`
	ToolCalls          int    `json:"tool_calls"`
	EvidenceItems      int    `json:"evidence_items"`
	TotalDuration      int64  `json:"total_duration_ns"`
	LoadDuration       int64  `json:"load_duration_ns"`
	PromptEvalCount    int    `json:"prompt_eval_count"`
	PromptEvalDuration int64  `json:"prompt_eval_duration_ns"`
	EvalCount          int    `json:"eval_count"`
	EvalDuration       int64  `json:"eval_duration_ns"`
}

type ollamaRun struct {
	content  string
	evidence []EvidenceRecord
	metadata ollamaRunMetadata
}

func (o *Ollama) Complete(ctx context.Context, req Request) (string, error) {
	o.runMu.Lock()
	defer o.runMu.Unlock()

	activeModel := o.Model
	run, err := o.doChatRun(ctx, activeModel, req)
	if err != nil && o.FallbackModel != "" && o.FallbackModel != o.Model {
		fmt.Fprintf(os.Stderr, "Primary model %q failed, trying fallback %q...\n", o.Model, o.FallbackModel)
		o.unloadModelBestEffort(o.Model)
		activeModel = o.FallbackModel
		run, err = o.doChatRun(ctx, activeModel, req)
	}
	if err != nil {
		if keepAliveUnloads(o.KeepAlive) {
			o.unloadModelBestEffort(activeModel)
		}
		return "", err
	}
	o.storeRun(run)
	if keepAliveUnloads(o.KeepAlive) {
		if err := o.unloadBestEffort(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: unloading Ollama model %q: %v\n", run.metadata.Model, err)
		}
	}
	return run.content, nil
}

func (o *Ollama) doChat(ctx context.Context, model string, req Request) (string, error) {
	run, err := o.doChatRun(ctx, model, req)
	if err != nil {
		return "", err
	}
	o.storeRun(run)
	return run.content, nil
}

func (o *Ollama) doChatRun(ctx context.Context, model string, req Request) (ollamaRun, error) {
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
			return ollamaRun{}, fmt.Errorf("marshaling tools: %w", err)
		}
		toolsJSON = data
	}

	maxIter := o.MaxIterations
	if maxIter <= 0 {
		maxIter = 20
	}
	run := ollamaRun{metadata: ollamaRunMetadata{Model: model}}
	options := o.requestOptions(req)
	keepAlive := o.chatKeepAlive()

	for iter := 0; iter < maxIter; iter++ {
		chatReq := ollamaChatRequest{
			Model:     model,
			Messages:  messages,
			Tools:     toolsJSON,
			Stream:    false, // streaming + tools is unreliable
			Options:   options,
			KeepAlive: keepAlive,
		}

		resp, err := o.sendChatResponse(ctx, chatReq)
		if err != nil {
			return ollamaRun{}, err
		}
		run.metadata.add(resp)
		respMsg := &resp.Message

		// No tool calls — return the content
		if len(respMsg.ToolCalls) == 0 {
			run.content = strings.TrimSpace(respMsg.Content)
			run.metadata.EvidenceItems = len(run.evidence)
			return run, nil
		}
		run.metadata.ToolCalls += len(respMsg.ToolCalls)

		// Append assistant message with tool calls
		messages = append(messages, *respMsg)

		// Execute each tool call and append results
		for _, tc := range respMsg.ToolCalls {
			args := tools.ParseToolArguments(tc.Function.Arguments)
			call := tools.ToolCall{
				Name:      tc.Function.Name,
				Arguments: args,
			}

			fmt.Fprintf(os.Stderr, "[tool] %s(%v)\n", call.Name, formatArgs(args))
			executor := o.Executor
			if executor == nil {
				executor = tools.NewExecutor(10)
			}
			result := executor.Execute(ctx, call)

			messages = append(messages, ollamaMessage{
				Role:    "tool",
				Content: result.Content,
			})
			if !result.IsError && strings.TrimSpace(result.Content) != "" {
				run.evidence = append(run.evidence, EvidenceRecord{
					Label:   evidenceLabel(call),
					Content: result.Content,
				})
			}
		}
	}

	// Hit max iterations — return whatever content we have from the last assistant message
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && messages[i].Content != "" {
			run.content = strings.TrimSpace(messages[i].Content)
			run.metadata.EvidenceItems = len(run.evidence)
			return run, nil
		}
	}

	return ollamaRun{}, fmt.Errorf("max tool iterations (%d) exceeded with no final response", maxIter)
}

func (o *Ollama) sendChat(ctx context.Context, req ollamaChatRequest) (*ollamaMessage, error) {
	resp, err := o.sendChatResponse(ctx, req)
	if err != nil {
		return nil, err
	}
	return &resp.Message, nil
}

func (o *Ollama) sendChatResponse(ctx context.Context, req ollamaChatRequest) (*ollamaChatResponse, error) {
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

	httpResp, err := o.httpClient().Do(httpReq)
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

	return &resp, nil
}

func (o *Ollama) httpClient() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return http.DefaultClient
}

func (m *ollamaRunMetadata) add(resp *ollamaChatResponse) {
	m.Calls++
	if resp.Model != "" {
		m.Model = resp.Model
	}
	m.TotalDuration += resp.TotalDuration
	m.LoadDuration += resp.LoadDuration
	m.PromptEvalCount += resp.PromptEvalCount
	m.PromptEvalDuration += resp.PromptEvalDuration
	m.EvalCount += resp.EvalCount
	m.EvalDuration += resp.EvalDuration
}

func (o *Ollama) requestOptions(req Request) ollamaOptions {
	numPredict := req.MaxTokens
	if o.NumPredict > 0 && (numPredict <= 0 || numPredict > o.NumPredict) {
		numPredict = o.NumPredict
	}
	return ollamaOptions{
		NumCtx:     o.NumCtx,
		NumPredict: numPredict,
	}
}

func (o *Ollama) chatKeepAlive() string {
	if keepAliveUnloads(o.KeepAlive) {
		// A zero keep-alive on every request would unload between tool-loop
		// turns. Keep the model resident for the loop and unload once Complete
		// returns instead.
		return ""
	}
	return strings.TrimSpace(o.KeepAlive)
}

func keepAliveUnloads(value string) bool {
	value = strings.TrimSpace(value)
	if value == "0" {
		return true
	}
	d, err := time.ParseDuration(value)
	return err == nil && d == 0
}

func evidenceLabel(call tools.ToolCall) string {
	args, err := json.Marshal(call.Arguments)
	if err != nil || string(args) == "null" {
		return call.Name
	}
	return call.Name + " " + string(args)
}

func (o *Ollama) storeRun(run ollamaRun) {
	metadata, _ := json.Marshal(run.metadata)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lastEvidence = append([]EvidenceRecord(nil), run.evidence...)
	o.lastMetadata = string(metadata)
	o.activeModel = run.metadata.Model
	o.loaded = true
}

// Unload releases the model used by the last successful run. It is safe to
// call more than once, which lets the hybrid pipeline enforce stage
// boundaries even when keep_alive already unloaded the model.
func (o *Ollama) Unload(ctx context.Context) error {
	o.mu.RLock()
	model := o.activeModel
	loaded := o.loaded
	o.mu.RUnlock()
	if !loaded {
		return nil
	}
	if model == "" {
		model = o.Model
	}
	if err := o.unloadModel(ctx, model); err != nil {
		return err
	}
	o.mu.Lock()
	if o.activeModel == model {
		o.loaded = false
	}
	o.mu.Unlock()
	return nil
}

func (o *Ollama) unloadBestEffort() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return o.Unload(ctx)
}

func (o *Ollama) unloadModelBestEffort(model string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := o.unloadModel(ctx, model); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: unloading failed Ollama model %q: %v\n", model, err)
	}
}

func (o *Ollama) unloadModel(ctx context.Context, model string) error {
	body, err := marshalUnloadRequest(model)
	if err != nil {
		return err
	}
	url := strings.TrimRight(o.Host, "/") + "/api/generate"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating unload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("ollama unload failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ollama unload returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func marshalUnloadRequest(model string) ([]byte, error) {
	body, err := json.Marshal(struct {
		Model     string `json:"model"`
		KeepAlive int    `json:"keep_alive"`
	}{Model: model, KeepAlive: 0})
	if err != nil {
		return nil, fmt.Errorf("marshaling unload request: %w", err)
	}
	return body, nil
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
