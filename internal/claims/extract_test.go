package claims

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/store"
)

// fakeOllama answers /api/chat with one claim per paragraph of the chunk,
// quoting its first sentence, unless fail says otherwise for the chunk.
type fakeOllama struct {
	mu      sync.Mutex
	chats   []map[string]any
	unloads int
	fail    func(chunk string) (status int, content string)
}

func (f *fakeOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/api/generate":
		if req["keep_alive"] == float64(0) {
			f.unloads++
		}
		json.NewEncoder(w).Encode(map[string]any{"done": true})
		return
	case "/api/chat":
	default:
		http.NotFound(w, r)
		return
	}
	f.chats = append(f.chats, req)
	msgs := req["messages"].([]any)
	user := msgs[len(msgs)-1].(map[string]any)["content"].(string)
	chunk := user
	if _, after, ok := strings.Cut(user, "\n\n"); ok && strings.HasPrefix(user, "Document title: ") {
		chunk = after
	}
	if f.fail != nil {
		if status, content := f.fail(chunk); status != 0 || content != "" {
			if status != 0 && status != 200 {
				w.WriteHeader(status)
				json.NewEncoder(w).Encode(map[string]string{"error": "runner crashed"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": content}})
			return
		}
	}
	type claim struct {
		Text     string `json:"text"`
		Quote    string `json:"quote"`
		AsOf     string `json:"as_of"`
		Volatile bool   `json:"volatile"`
	}
	var cs []claim
	for _, p := range strings.Split(chunk, "\n\n") {
		first, _, _ := strings.Cut(p, ". ")
		cs = append(cs, claim{Text: "It says " + first, Quote: first, AsOf: "2024-13"})
	}
	content, _ := json.Marshal(map[string]any{"claims": cs})
	json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": string(content)}})
}

func (f *fakeOllama) chatCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.chats)
}

func newFake(t *testing.T) (*fakeOllama, *Extractor) {
	t.Helper()
	f := &fakeOllama{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, &Extractor{Host: srv.URL + "/", Model: "qwen-test", ChunkChars: 120, MaxChunks: 8}
}

func TestChunk(t *testing.T) {
	text := "Alpha one. Alpha two.\n\nBeta paragraph here.\n \nGamma " + strings.Repeat("long sentence goes on. ", 12) + "\n\nDelta."
	a := Chunk(text, 80, 10)
	if b := Chunk(text, 80, 10); strings.Join(a, "|") != strings.Join(b, "|") {
		t.Fatal("chunking isn't deterministic")
	}
	if len(a) < 3 {
		t.Fatalf("chunks = %q", a)
	}
	if a[0] != "Alpha one. Alpha two.\n\nBeta paragraph here." {
		t.Errorf("paragraphs aren't packed: %q", a[0])
	}
	for _, c := range a {
		if n := utf8.RuneCountInString(c); n > 80 || n == 0 {
			t.Errorf("chunk of %d runes: %q", n, c)
		}
	}
	// The long paragraph is cut after a sentence, not mid-word.
	if !strings.HasSuffix(a[1], ".") {
		t.Errorf("long paragraph cut mid-sentence: %q", a[1])
	}
	if got := Chunk(text, 80, 2); len(got) != 2 || got[1] != a[1] {
		t.Errorf("max chunks: %q", got)
	}
	if got := Chunk("  \n\n ", 80, 2); len(got) != 0 {
		t.Errorf("blank text = %q", got)
	}
	// A run with no spaces is cut at the size.
	if got := Chunk(strings.Repeat("é", 50), 20, 9); len(got) != 3 || utf8.RuneCountInString(got[0]) != 20 {
		t.Errorf("unbroken text = %q", got)
	}
}

func TestExtractChunk_RequestAndParsing(t *testing.T) {
	f, ex := newFake(t)
	f.fail = func(string) (int, string) {
		cs := []map[string]any{{"text": " A ", "quote": " q ", "as_of": "2024-03", "volatile": true}, {"text": "", "quote": "x"}, {"text": "y", "quote": " "}}
		for range 20 {
			cs = append(cs, map[string]any{"text": "t", "quote": "q", "as_of": "March 2024"})
		}
		b, _ := json.Marshal(map[string]any{"claims": cs})
		return 200, string(b)
	}
	got, err := ex.ExtractChunk(context.Background(), "Title", "chunk text")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxPerChunk || got[0] != (store.ExtractedClaim{Text: "A", Quote: "q", AsOf: "2024-03", Volatile: true}) || got[1].AsOf != "" {
		t.Fatalf("claims = %+v", got)
	}
	req := f.chats[0]
	opts := req["options"].(map[string]any)
	if req["model"] != "qwen-test" || req["think"] != false || req["stream"] != false || req["format"] == nil || opts["temperature"] != float64(0) {
		t.Errorf("request = %v", req)
	}
	msgs := req["messages"].([]any)
	if sys := msgs[0].(map[string]any)["content"].(string); !strings.Contains(sys, "bibliography") {
		t.Error("prompt doesn't say to skip references")
	}
	if user := msgs[1].(map[string]any)["content"].(string); user != "Document title: Title\n\nchunk text" {
		t.Errorf("user message = %q", user)
	}
}

func TestExtractChunk_Errors(t *testing.T) {
	f, ex := newFake(t)
	var fatal errFatal
	for _, tc := range []struct {
		status  int
		content string
		fatal   bool
	}{
		{500, "", false},
		{404, "", true},
		{200, "{not json", false},
	} {
		f.fail = func(string) (int, string) { return tc.status, tc.content }
		_, err := ex.ExtractChunk(context.Background(), "", "x")
		if err == nil || errors.As(err, &fatal) != tc.fatal {
			t.Errorf("status %d %q: err = %v, fatal = %v", tc.status, tc.content, err, errors.As(err, &fatal))
		}
	}
	down := &Extractor{Host: "http://127.0.0.1:1", Model: "m"}
	if _, err := down.ExtractChunk(context.Background(), "", "x"); !errors.As(err, &fatal) {
		t.Errorf("Ollama down: err = %v, want fatal", err)
	}
}

func TestIsoDate(t *testing.T) {
	for in, want := range map[string]string{"2024": "2024", "2024-03": "2024-03", " 2024-03-09 ": "2024-03-09", "2024-13": "", "2024-02-32": "", "March 2024": "", "": "", "24": ""} {
		if got := isoDate(in); got != want {
			t.Errorf("isoDate(%q) = %q, want %q", in, got, want)
		}
	}
}

const threeChunks = "First claim here. More.\n\nSecond FAIL claim. More.\n\nThird claim here. More."

func TestExtract_RetriesOnlyFailedChunks(t *testing.T) {
	f, ex := newFake(t)
	ex.ChunkChars = 30
	ctx := context.Background()
	if n := len(Chunk(threeChunks, 30, 8)); n != 3 {
		t.Fatalf("fixture has %d chunks", n)
	}
	f.fail = func(chunk string) (int, string) {
		if strings.Contains(chunk, "FAIL") {
			return 500, ""
		}
		return 0, ""
	}
	e, ran, err := ex.Extract(ctx, "T", "sha", threeChunks, nil, nil)
	if err != nil || !ran {
		t.Fatal(ran, err)
	}
	if e.Extractor != "qwen-test/claims-v1" || e.Chunks != 3 || e.Attempts != 1 || len(e.Claims) != 2 || len(e.Failed) != 1 || e.Failed[0].Chunk != 1 || e.Complete() {
		t.Fatalf("first = %+v", e)
	}
	if e.Claims[1].Chunk != 2 || e.Claims[1].Quote != "Third claim here" || e.Claims[1].AsOf != "" {
		t.Errorf("claims = %+v", e.Claims)
	}

	f.fail = nil
	before := f.chatCount()
	e2, ran, err := ex.Extract(ctx, "T", "sha", threeChunks, &e, nil)
	if err != nil || !ran || f.chatCount()-before != 1 {
		t.Fatalf("retry: ran %v err %v, %d calls", ran, err, f.chatCount()-before)
	}
	if !e2.Complete() || e2.Attempts != 2 || len(e2.Claims) != 3 || e2.Claims[1].Chunk != 1 || e2.Claims[2].Chunk != 2 {
		t.Fatalf("retry = %+v", e2)
	}
}

func TestExtract_PauseIsNotAnAttempt(t *testing.T) {
	f, ex := newFake(t)
	ex.ChunkChars = 30
	calls := 0
	pause := func() bool { calls++; return calls > 1 }
	e, ran, err := ex.Extract(context.Background(), "", "sha", threeChunks, nil, pause)
	if err != nil || !ran || f.chatCount() != 1 {
		t.Fatalf("ran %v err %v calls %d", ran, err, f.chatCount())
	}
	if e.Attempts != 0 || len(e.Failed) != 2 || e.Failed[0].Error != Interrupted || !interrupted(e) {
		t.Fatalf("paused = %+v", e)
	}
	// Ollama going away mid-text keeps what was done and returns the error.
	f.fail = func(chunk string) (int, string) {
		if strings.Contains(chunk, "FAIL") {
			return 404, ""
		}
		return 0, ""
	}
	e, ran, err = ex.Extract(context.Background(), "", "sha", threeChunks, nil, nil)
	var fatal errFatal
	if !errors.As(err, &fatal) || !ran || e.Attempts != 0 || len(e.Claims) != 1 || len(e.Failed) != 2 || e.Failed[1].Error != Interrupted {
		t.Fatalf("fatal: ran %v err %v e %+v", ran, err, e)
	}
}

func TestNew_FallsBack(t *testing.T) {
	cfg := &config.Config{}
	cfg.Ollama.Host = "http://gpu:11434"
	cfg.Ollama.UtilityModel = "qwen3.5:9b"
	cfg.Ollama.Model = "big"
	ex := New(cfg)
	if ex.Host != "http://gpu:11434" || ex.Model != "qwen3.5:9b" || ex.Name() != "qwen3.5:9b/claims-v1" {
		t.Errorf("extractor = %+v", ex)
	}
	cfg.Store.Claims.Model = "other"
	cfg.Ollama.Host = ""
	if ex := New(cfg); ex.Model != "other" || ex.Host != DefaultHost {
		t.Errorf("extractor = %+v", ex)
	}
}
