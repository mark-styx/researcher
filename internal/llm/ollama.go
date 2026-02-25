package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Ollama uses the Ollama HTTP API for completions.
type Ollama struct {
	Host          string
	Model         string
	FallbackModel string
}

func (o *Ollama) Name() string {
	return "ollama"
}

type ollamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	System string `json:"system,omitempty"`
	Stream bool   `json:"stream"`
}

type ollamaResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
	Error    string `json:"error,omitempty"`
}

func (o *Ollama) Complete(ctx context.Context, req Request) (string, error) {
	resp, err := o.doGenerate(ctx, o.Model, req)
	if err != nil && o.FallbackModel != "" && o.FallbackModel != o.Model {
		fmt.Printf("Primary model %q failed, trying fallback %q...\n", o.Model, o.FallbackModel)
		resp, err = o.doGenerate(ctx, o.FallbackModel, req)
	}
	return resp, err
}

func (o *Ollama) doGenerate(ctx context.Context, model string, req Request) (string, error) {
	body := ollamaRequest{
		Model:  model,
		Prompt: req.UserPrompt,
		System: req.SystemPrompt,
		Stream: true,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshaling request: %w", err)
	}

	url := strings.TrimRight(o.Host, "/") + "/api/generate"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("ollama request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return "", fmt.Errorf("ollama returned %d: %s", httpResp.StatusCode, string(respBody))
	}

	// Stream response, concatenate chunks
	var result strings.Builder
	decoder := json.NewDecoder(httpResp.Body)
	for {
		var chunk ollamaResponse
		if err := decoder.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			return result.String(), fmt.Errorf("decoding response: %w", err)
		}
		if chunk.Error != "" {
			return result.String(), fmt.Errorf("ollama error: %s", chunk.Error)
		}
		result.WriteString(chunk.Response)
		if chunk.Done {
			break
		}
	}

	return strings.TrimSpace(result.String()), nil
}
