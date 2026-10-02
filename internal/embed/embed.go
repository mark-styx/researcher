// Package embed turns passage text into vectors for the store index, using
// an Ollama embedding model (nomic-embed-text by default, the model grepai
// uses).
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/marklubin/researchguy/internal/config"
)

// Embedder embeds documents (passages) and queries.
type Embedder interface {
	Model() string
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// Ollama embeds through Ollama's /api/embed.
type Ollama struct {
	Host      string
	ModelName string
	HTTP      *http.Client
	// BatchSize is how many texts go in one request.
	BatchSize int
}

// DefaultHost is Ollama's address when neither store.embed.host nor
// ollama.host is set.
const DefaultHost = "http://localhost:11434"

// New returns the embedder store.embed configures; a blank host uses
// ollama.host.
func New(cfg *config.Config) *Ollama {
	host := cfg.Store.Embed.Host
	if strings.TrimSpace(host) == "" {
		host = cfg.Ollama.Host
	}
	if strings.TrimSpace(host) == "" {
		host = DefaultHost
	}
	model := cfg.Store.Embed.Model
	if model == "" {
		model = "nomic-embed-text"
	}
	return &Ollama{Host: host, ModelName: model}
}

// Model is the model name vectors are stored under.
func (o *Ollama) Model() string { return o.ModelName }

// prefixes are the task prefixes nomic-embed-text was trained with; its
// retrieval quality depends on them. Other models get the text as-is.
func (o *Ollama) prefixes() (doc, query string) {
	if strings.Contains(o.ModelName, "nomic-embed") {
		return "search_document: ", "search_query: "
	}
	return "", ""
}

// EmbedDocuments embeds passages, in batches.
func (o *Ollama) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	doc, _ := o.prefixes()
	size := o.BatchSize
	if size <= 0 {
		size = 64
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += size {
		batch := texts[start:min(start+size, len(texts))]
		in := make([]string, len(batch))
		for i, t := range batch {
			in[i] = doc + t
		}
		vs, err := o.embed(ctx, in)
		if err != nil {
			return out, err
		}
		out = append(out, vs...)
	}
	return out, nil
}

// EmbedQuery embeds a search query.
func (o *Ollama) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	_, q := o.prefixes()
	vs, err := o.embed(ctx, []string{q + text})
	if err != nil {
		return nil, err
	}
	return vs[0], nil
}

func (o *Ollama) embed(ctx context.Context, input []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"model": o.ModelName, "input": input, "truncate": true})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.Host, "/")+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := o.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding with %s at %s: %w", o.ModelName, o.Host, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, fmt.Errorf("reading embeddings: %w", err)
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
		Error      string      `json:"error"`
	}
	if err := json.Unmarshal(data, &out); err != nil && resp.StatusCode == http.StatusOK {
		return nil, fmt.Errorf("decoding embeddings: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := out.Error
		if msg == "" {
			msg = strings.TrimSpace(string(data[:min(len(data), 200)]))
		}
		return nil, fmt.Errorf("embedding with %s: HTTP %d: %s", o.ModelName, resp.StatusCode, msg)
	}
	if len(out.Embeddings) != len(input) {
		return nil, fmt.Errorf("embedding with %s: got %d vectors for %d texts", o.ModelName, len(out.Embeddings), len(input))
	}
	return out.Embeddings, nil
}
