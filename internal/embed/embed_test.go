package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/marklubin/researchguy/internal/config"
)

type fakeOllama struct {
	mu       sync.Mutex
	requests []map[string]any
	status   int
	short    bool
}

func (f *fakeOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/embed" {
		http.NotFound(w, r)
		return
	}
	var req map[string]any
	json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	if f.status != 0 {
		w.WriteHeader(f.status)
		json.NewEncoder(w).Encode(map[string]string{"error": "model not found"})
		return
	}
	in := req["input"].([]any)
	n := len(in)
	if f.short {
		n--
	}
	vs := make([][]float32, n)
	for i := range vs {
		vs[i] = []float32{float32(len(in[i].(string))), 1}
	}
	json.NewEncoder(w).Encode(map[string]any{"embeddings": vs})
}

func TestEmbedDocuments_BatchesAndPrefixes(t *testing.T) {
	f := &fakeOllama{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	o := &Ollama{Host: srv.URL + "/", ModelName: "nomic-embed-text", BatchSize: 2}
	vs, err := o.EmbedDocuments(context.Background(), []string{"a", "bb", "ccc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 3 || len(f.requests) != 2 {
		t.Fatalf("%d vectors in %d requests, want 3 in 2", len(vs), len(f.requests))
	}
	first := f.requests[0]["input"].([]any)[0].(string)
	if first != "search_document: a" {
		t.Errorf("input = %q, want the nomic document prefix", first)
	}
	if f.requests[0]["model"] != "nomic-embed-text" || f.requests[0]["truncate"] != true {
		t.Errorf("request = %v", f.requests[0])
	}
	if vs[2][0] != float32(len("search_document: ccc")) {
		t.Errorf("vectors out of order: %v", vs)
	}
	q, err := o.EmbedQuery(context.Background(), "what")
	if err != nil || len(q) != 2 {
		t.Fatalf("EmbedQuery = %v, %v", q, err)
	}
	if got := f.requests[2]["input"].([]any)[0].(string); got != "search_query: what" {
		t.Errorf("query input = %q", got)
	}
}

func TestEmbed_OtherModelsGetNoPrefix(t *testing.T) {
	f := &fakeOllama{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	o := &Ollama{Host: srv.URL, ModelName: "mxbai-embed-large"}
	if _, err := o.EmbedDocuments(context.Background(), []string{"plain"}); err != nil {
		t.Fatal(err)
	}
	if got := f.requests[0]["input"].([]any)[0].(string); got != "plain" {
		t.Errorf("input = %q", got)
	}
}

func TestEmbed_Errors(t *testing.T) {
	f := &fakeOllama{status: http.StatusNotFound}
	srv := httptest.NewServer(f)
	o := &Ollama{Host: srv.URL, ModelName: "nomic-embed-text"}
	if _, err := o.EmbedDocuments(context.Background(), []string{"x"}); err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Errorf("404: err = %v", err)
	}
	f.status, f.short = 0, true
	if _, err := o.EmbedDocuments(context.Background(), []string{"x", "y"}); err == nil || !strings.Contains(err.Error(), "got 1 vectors for 2") {
		t.Errorf("short answer: err = %v", err)
	}
	srv.Close()
	if _, err := o.EmbedQuery(context.Background(), "x"); err == nil {
		t.Error("down server: no error")
	}
}

func TestNew_HostFallsBackToOllama(t *testing.T) {
	cfg := &config.Config{Ollama: config.OllamaConfig{Host: "http://ollama:11434"}}
	o := New(cfg)
	if o.Host != "http://ollama:11434" || o.Model() != "nomic-embed-text" {
		t.Errorf("New = %+v", o)
	}
	cfg.Store.Embed = config.StoreEmbedConfig{Host: "http://other", Model: "m"}
	if o := New(cfg); o.Host != "http://other" || o.Model() != "m" {
		t.Errorf("New with store.embed = %+v", o)
	}
	if o := New(&config.Config{}); o.Host != DefaultHost {
		t.Errorf("New with no host = %q, want %q", o.Host, DefaultHost)
	}
}
