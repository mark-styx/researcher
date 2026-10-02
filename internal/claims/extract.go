// Package claims extracts checkable claims from the store's documents with
// a local model, and labels how claims from different origins relate.
// Extractions and links are written to the store (claims/, links.jsonl);
// the index anchors each claim's quote in its document's text, so nothing
// here trusts the model's quote.
package claims

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/store"
)

// PromptVersion names the extraction prompt. Changing the prompt or the
// chunking in a way that changes what's extracted means a new version, so
// the new extraction goes in new files rather than mixing with the old.
const PromptVersion = "claims-v1"

// MaxPerChunk is the most claims kept from one chunk.
const MaxPerChunk = 15

// DefaultHost is Ollama's address when neither store.claims.host nor
// ollama.host is set.
const DefaultHost = "http://localhost:11434"

// Extractor extracts claims through an Ollama model's structured output.
type Extractor struct {
	Host  string
	Model string
	HTTP  *http.Client
	// ChunkChars and MaxChunks are how a text is split (see Chunk).
	ChunkChars int
	MaxChunks  int
	NumCtx     int
}

// New returns the extractor store.claims configures.
func New(cfg *config.Config) *Extractor {
	c := cfg.Store.Claims
	host := firstNonBlank(c.Host, cfg.Ollama.Host, DefaultHost)
	model := firstNonBlank(c.Model, cfg.Ollama.UtilityModel, cfg.Ollama.Model)
	return &Extractor{Host: host, Model: model, ChunkChars: c.ChunkChars, MaxChunks: c.MaxChunks}
}

func firstNonBlank(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Name is the extractor recorded with each extraction: the model and the
// prompt version.
func (e *Extractor) Name() string { return e.Model + "/" + PromptVersion }

const systemPrompt = `You extract checkable claims from a document. A claim is one factual assertion the document makes, stated on its own so it makes sense without the document. For each claim give:
- text: the claim in one sentence
- quote: the exact words from the document that state it, copied character for character, one sentence or less. Do not add, drop or change words, numbers or footnote marks, and do not shorten it with an ellipsis.
- as_of: the date the claim is true as of, if the document says (YYYY, YYYY-MM or YYYY-MM-DD), else ""
- volatile: true if the claim's truth changes with time (a status, a count to date, a price, who holds an office), else false
Only extract what the document asserts. Skip navigation, menus, references, bibliography and citation entries, figure captions, opinions and questions. If the text around a claim is garbled or a value is missing, skip the claim rather than filling it in. At most 15 claims.`

var schema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"claims": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text":     map[string]any{"type": "string"},
					"quote":    map[string]any{"type": "string"},
					"as_of":    map[string]any{"type": "string"},
					"volatile": map[string]any{"type": "boolean"},
				},
				"required": []string{"text", "quote", "as_of", "volatile"},
			},
		},
	},
	"required": []string{"claims"},
}

// errFatal marks an error that isn't the chunk's fault (Ollama down, the
// model missing): the pass stops and the chunk isn't counted as failed.
type errFatal struct{ err error }

func (e errFatal) Error() string { return e.err.Error() }
func (e errFatal) Unwrap() error { return e.err }

// ExtractChunk asks the model for the claims in one chunk.
func (e *Extractor) ExtractChunk(ctx context.Context, title, chunk string) ([]store.ExtractedClaim, error) {
	user := chunk
	if strings.TrimSpace(title) != "" {
		user = "Document title: " + strings.TrimSpace(title) + "\n\n" + chunk
	}
	numCtx := e.NumCtx
	if numCtx <= 0 {
		numCtx = 16384
	}
	body, err := json.Marshal(map[string]any{
		"model":      e.Model,
		"stream":     false,
		"think":      false,
		"format":     schema,
		"keep_alive": "5m",
		"options":    map[string]any{"temperature": 0, "num_ctx": numCtx},
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": user},
		},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.Host, "/")+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, errFatal{err}
	}
	req.Header.Set("Content-Type", "application/json")
	client := e.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errFatal{fmt.Errorf("extracting claims with %s at %s: %w", e.Model, e.Host, err)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("reading the model's answer: %w", err)
	}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Error string `json:"error"`
	}
	jerr := json.Unmarshal(data, &out)
	if resp.StatusCode != http.StatusOK {
		msg := out.Error
		if msg == "" {
			msg = strings.TrimSpace(string(data[:min(len(data), 200)]))
		}
		err := fmt.Errorf("extracting claims with %s: HTTP %d: %s", e.Model, resp.StatusCode, msg)
		if resp.StatusCode < 500 {
			return nil, errFatal{err}
		}
		return nil, err
	}
	if jerr != nil {
		return nil, fmt.Errorf("decoding the model's answer: %w", jerr)
	}
	var parsed struct {
		Claims []struct {
			Text     string `json:"text"`
			Quote    string `json:"quote"`
			AsOf     string `json:"as_of"`
			Volatile bool   `json:"volatile"`
		} `json:"claims"`
	}
	if err := json.Unmarshal([]byte(out.Message.Content), &parsed); err != nil {
		return nil, fmt.Errorf("the model's claims aren't valid JSON: %w", err)
	}
	var claims []store.ExtractedClaim
	for _, c := range parsed.Claims {
		text, q := strings.TrimSpace(c.Text), strings.TrimSpace(c.Quote)
		if text == "" || q == "" {
			continue
		}
		claims = append(claims, store.ExtractedClaim{Text: text, Quote: q, AsOf: isoDate(c.AsOf), Volatile: c.Volatile})
		if len(claims) == MaxPerChunk {
			break
		}
	}
	return claims, nil
}

var isoDateRE = regexp.MustCompile(`^\d{4}(-(0[1-9]|1[0-2])(-(0[1-9]|[12]\d|3[01]))?)?$`)

// isoDate is s when it's YYYY, YYYY-MM or YYYY-MM-DD, otherwise blank: a
// date the model wrote any other way isn't guessed at.
func isoDate(s string) string {
	s = strings.TrimSpace(s)
	if isoDateRE.MatchString(s) {
		return s
	}
	return ""
}

// Unload asks Ollama to drop the model from memory, so a research task
// that starts next has the GPU.
func (e *Extractor) Unload(ctx context.Context) error {
	body, _ := json.Marshal(map[string]any{"model": e.Model, "keep_alive": 0})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.Host, "/")+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := e.HTTP
	if client == nil {
		client = &http.Client{Timeout: time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Interrupted is the error a chunk the pass didn't reach is recorded
// with. Unlike a model failure it doesn't count as an attempt.
const Interrupted = "not reached: the pass stopped first"

// Extract extracts claims from text, or, given the text's previous
// extraction, retries only the chunks that one failed on. It stops before
// the next chunk when ctx ends or pause says so, recording the chunks it
// didn't reach as Interrupted. ran is false when no chunk got an answer,
// so there's nothing new to write. A fatal error (Ollama down, the model
// missing) is returned along with whatever was done before it.
func (e *Extractor) Extract(ctx context.Context, title, sha, text string, prev *store.Extraction, pause func() bool) (ex store.Extraction, ran bool, err error) {
	chunks := Chunk(text, e.ChunkChars, e.MaxChunks)
	ex = store.Extraction{TextSHA: sha, Extractor: e.Name(), Model: e.Model, Chunks: len(chunks)}
	todo := make([]int, len(chunks))
	for i := range todo {
		todo[i] = i
	}
	if prev != nil {
		ex.Attempts = prev.Attempts
		ex.DurationMS = prev.DurationMS
		if prev.Chunks == len(chunks) {
			todo = todo[:0]
			retry := map[int]bool{}
			for _, f := range prev.Failed {
				retry[f.Chunk] = true
			}
			for i := range chunks {
				if retry[i] {
					todo = append(todo, i)
				}
			}
			for _, c := range prev.Claims {
				if !retry[c.Chunk] {
					ex.Claims = append(ex.Claims, c)
				}
			}
		}
	}
	start := time.Now()
	stopped := false
	for _, i := range todo {
		if stopped || ctx.Err() != nil || (pause != nil && pause()) {
			stopped = true
			ex.Failed = append(ex.Failed, store.ChunkError{Chunk: i, Error: Interrupted})
			continue
		}
		cs, cerr := e.ExtractChunk(ctx, title, chunks[i])
		var fatal errFatal
		switch {
		case cerr == nil:
			ran = true
			for _, c := range cs {
				c.Chunk = i
				ex.Claims = append(ex.Claims, c)
			}
		case ctx.Err() != nil:
			stopped = true
			ex.Failed = append(ex.Failed, store.ChunkError{Chunk: i, Error: Interrupted})
		case errors.As(cerr, &fatal):
			stopped = true
			err = cerr
			ex.Failed = append(ex.Failed, store.ChunkError{Chunk: i, Error: Interrupted})
		default:
			ran = true
			ex.Failed = append(ex.Failed, store.ChunkError{Chunk: i, Error: cerr.Error()})
		}
	}
	// A pass that got through its chunks is an attempt; one cut short
	// isn't, so the chunks it didn't reach keep their tries.
	if !stopped && len(todo) > 0 {
		ex.Attempts++
	}
	ex.DurationMS += time.Since(start).Milliseconds()
	ex.ExtractedAt = time.Now().UTC()
	slices.SortStableFunc(ex.Claims, func(a, b store.ExtractedClaim) int { return a.Chunk - b.Chunk })
	slices.SortFunc(ex.Failed, func(a, b store.ChunkError) int { return a.Chunk - b.Chunk })
	return ex, ran, err
}

// Chunk splits text into chunks of at most size characters (runes), at
// paragraph breaks where it can, then sentence ends, then spaces, and
// keeps the first max of them. The same text and settings always give the
// same chunks, since a claim's id depends on its chunk.
func Chunk(text string, size, max int) []string {
	if size <= 0 {
		size = 6000
	}
	if max <= 0 {
		max = 8
	}
	var pieces []string
	for _, para := range paragraphRE.Split(text, -1) {
		para = strings.TrimSpace(para)
		for para != "" {
			if utf8.RuneCountInString(para) <= size {
				pieces = append(pieces, para)
				break
			}
			cut := splitPoint(para, size)
			pieces = append(pieces, strings.TrimSpace(para[:cut]))
			para = strings.TrimSpace(para[cut:])
		}
	}
	var chunks []string
	var cur strings.Builder
	curRunes := 0
	for _, p := range pieces {
		n := utf8.RuneCountInString(p)
		if curRunes > 0 && curRunes+2+n > size {
			chunks = append(chunks, cur.String())
			if len(chunks) == max {
				return chunks
			}
			cur.Reset()
			curRunes = 0
		}
		if curRunes > 0 {
			cur.WriteString("\n\n")
			curRunes += 2
		}
		cur.WriteString(p)
		curRunes += n
	}
	if curRunes > 0 && len(chunks) < max {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

var paragraphRE = regexp.MustCompile(`\n[ \t]*\n`)

// splitPoint is the byte offset to cut a paragraph longer than size runes
// at: the last sentence end in its first size runes, else the last space,
// else size runes in.
func splitPoint(s string, size int) int {
	limit := len(s)
	n := 0
	for i := range s {
		if n == size {
			limit = i
			break
		}
		n++
	}
	head := s[:limit]
	for _, sep := range []string{". ", "? ", "! ", "\n"} {
		if i := strings.LastIndex(head, sep); i > limit/2 {
			return i + len(sep)
		}
	}
	if i := strings.LastIndex(head, " "); i > limit/2 {
		return i + 1
	}
	return limit
}
