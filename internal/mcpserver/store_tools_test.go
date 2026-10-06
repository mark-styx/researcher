package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/embed"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
	"github.com/marklubin/researchguy/internal/store/index/indextest"
	"github.com/marklubin/researchguy/internal/store/retrieve"
)

func readClient(t *testing.T, cfg *config.Config) *client.Client {
	t.Helper()
	return profileClient(t, NewRead(cfg, "test"))
}

func profileClient(t *testing.T, s *server.MCPServer) *client.Client {
	t.Helper()
	c, err := client.NewInProcessClient(s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err := c.Initialize(context.Background(), mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: "2024-11-05", ClientInfo: mcp.Implementation{Name: "test", Version: "1"}}}); err != nil {
		t.Fatal(err)
	}
	return c
}

func callOn(t *testing.T, c *client.Client, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := c.CallTool(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: tool, Arguments: args}})
	if err != nil {
		t.Fatalf("calling %s: %v", tool, err)
	}
	return res
}

func TestReadProfile_OnlyReadTools(t *testing.T) {
	c := readClient(t, testConfig(t))
	res, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := slices.Clone(ReadTools)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("read profile tools = %v, want %v", names, want)
	}
}

// The ingest profile is the read profile plus researchguy_ingest_url: an
// agent researching a topic can fetch pages into the store, but can't start
// research or write the graph.
func TestIngestProfile_ReadToolsAndIngest(t *testing.T) {
	c := profileClient(t, NewIngest(testConfig(t), "test"))
	res, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := append(slices.Clone(ReadTools), "researchguy_ingest_url")
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("ingest profile tools = %v, want %v", names, want)
	}
	got := slices.Clone(IngestTools)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("IngestTools = %v, want %v", got, want)
	}

	ing := callOn(t, c, "researchguy_ingest_url", map[string]any{"url": "https://example.com/"})
	if !ing.IsError || !strings.Contains(extractText(t, ing), "store.dsn is not set") {
		t.Errorf("ingest without a dsn = %s", extractText(t, ing))
	}
}

func TestStoreTools_WithoutDSN(t *testing.T) {
	c := readClient(t, testConfig(t))
	res := callOn(t, c, "researchguy_find", map[string]any{"query": "x"})
	if !res.IsError || !strings.Contains(extractText(t, res), "store.dsn is not set") {
		t.Errorf("find without a dsn = %s", extractText(t, res))
	}
}

// storeSite serves a page and a fake Ollama /api/embed.
func storeSite(t *testing.T) *httptest.Server {
	t.Helper()
	text := strings.Repeat("Groundwater nitrate levels rose near the feedlots, the county survey found. ", 30)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/survey":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<html><head><title>Nitrate survey</title><meta name="citation_publication_date" content="2022-08-01"></head><body><article><p>%s</p></article></body></html>`, text)
		case "/api/embed":
			var req struct{ Input []string }
			json.NewDecoder(r.Body).Decode(&req)
			out := make([][]float32, len(req.Input))
			for i := range out {
				out[i] = make([]float32, index.Dims)
				out[i][0] = 1
			}
			json.NewEncoder(w).Encode(map[string]any{"embeddings": out})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStoreTools_IngestFindAndLookups(t *testing.T) {
	t.Setenv("RESEARCHGUY_ALLOW_PRIVATE_URLS", "true")
	srv := storeSite(t)
	cfg := testConfig(t)
	cfg.Store.Dir = filepath.Join(t.TempDir(), "store")
	cfg.Store.DSN = indextest.DSN(t)
	cfg.Store.Embed.Host = srv.URL
	full := setupClient(t, cfg, &mockProvider{})

	var ing retrieve.IngestResult
	decode(t, callOn(t, full, "researchguy_ingest_url", map[string]any{"url": srv.URL + "/survey"}), &ing)
	if len(ing.Documents) != 1 || len(ing.Documents[0].Passages) == 0 || ing.Embedded == 0 {
		t.Fatalf("ingest = %+v", ing)
	}
	doc := ing.Documents[0]

	// The read profile finds it and looks it up, but can't ingest.
	c := readClient(t, cfg)
	var res retrieve.Result
	decode(t, callOn(t, c, "researchguy_find", map[string]any{"query": "nitrate groundwater", "since": "2022", "kinds": []any{"passage"}}), &res)
	if res.Mode != retrieve.ModeHybrid || len(res.Cards) == 0 || res.Cards[0].DocumentID != doc.DocumentID ||
		res.Cards[0].Published == nil || res.Cards[0].Published.Date != "2022-08-01" {
		t.Fatalf("find = %+v", res)
	}
	card := res.Cards[0]
	decode(t, callOn(t, c, "researchguy_find", map[string]any{"query": "nitrate", "until": "2021"}), &res)
	if len(res.Cards) != 0 {
		t.Errorf("until 2021 found %d cards", len(res.Cards))
	}

	var p retrieve.PassageResult
	decode(t, callOn(t, c, "researchguy_passage", map[string]any{"id": card.Ref}), &p)
	if p.PassageID != card.PassageID {
		t.Errorf("passage = %+v", p)
	}
	var d retrieve.DocumentResult
	decode(t, callOn(t, c, "researchguy_document", map[string]any{"id": fmt.Sprint(doc.DocumentID), "chars": 50}), &d)
	if len([]rune(d.Text)) != 50 || !d.Truncated || d.Title != "Nitrate survey" {
		t.Errorf("document = %+v", d)
	}
	var s retrieve.SourceResult
	decode(t, callOn(t, c, "researchguy_source", map[string]any{"ref": srv.URL + "/survey#top"}), &s)
	if s.ID != doc.SourceID || len(s.Fetches) != 1 || s.Fetches[0].Reason != store.ReasonIngest {
		t.Errorf("source = %+v", s)
	}

	// A claim extracted from the page is found, looked up and laid out.
	ctx := context.Background()
	st, err := store.Open(cfg.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Open(ctx, cfg.Store.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	var sha string
	if err := ix.Pool().QueryRow(ctx, `SELECT sha256 FROM documents WHERE id = $1`, doc.DocumentID).Scan(&sha); err != nil {
		t.Fatal(err)
	}
	if err := st.PutExtraction(store.Extraction{TextSHA: sha, Extractor: "test/claims-v1", Model: "test", Chunks: 1, Attempts: 1,
		Claims: []store.ExtractedClaim{{Text: "Nitrate levels rose near the feedlots.", Quote: "Groundwater nitrate levels rose near the feedlots"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.SyncClaims(ctx, st); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.EmbedClaims(ctx, st, embed.New(cfg)); err != nil {
		t.Fatal(err)
	}
	claimID := index.ClaimID(doc.DocumentID, "test/claims-v1", 0, 0)
	decode(t, callOn(t, c, "researchguy_find", map[string]any{"query": "nitrate feedlots", "kinds": []any{"claim"}}), &res)
	if len(res.Cards) != 1 || res.Cards[0].Ref != retrieve.ClaimRef(claimID) || !res.Cards[0].QuoteVerified ||
		!slices.Equal(res.Cards[0].Flags, []string{retrieve.FlagSingleOrigin}) {
		t.Fatalf("find claims = %+v", res.Cards)
	}
	var cl retrieve.ClaimResult
	decode(t, callOn(t, c, "researchguy_claim", map[string]any{"id": retrieve.ClaimRef(claimID)}), &cl)
	if cl.ClaimID != claimID || cl.Extractor != "test/claims-v1" || cl.Passage == nil || cl.Quote != "Groundwater nitrate levels rose near the feedlots" {
		t.Errorf("claim = %+v", cl)
	}
	var tl retrieve.TimelineResult
	decode(t, callOn(t, c, "researchguy_timeline", map[string]any{"query": "nitrate feedlots"}), &tl)
	if len(tl.Entries) != 1 || tl.Entries[0].Dated != "2022-08-01" || !tl.Entries[0].Matched {
		t.Errorf("timeline = %+v", tl)
	}

	for _, bad := range []struct {
		tool string
		args map[string]any
	}{
		{"researchguy_claim", map[string]any{"id": "C:x"}},
		{"researchguy_claim", map[string]any{"id": "C:123"}},
		{"researchguy_timeline", map[string]any{"query": "C:123"}},
		{"researchguy_timeline", map[string]any{"query": "x", "since": "soon"}},
		{"researchguy_find", map[string]any{"query": "x", "since": "soon"}},
		{"researchguy_find", map[string]any{"query": "x", "kinds": []any{"tweet"}}},
		{"researchguy_passage", map[string]any{"id": "P:abc"}},
		{"researchguy_passage", map[string]any{"id": "123"}},
		{"researchguy_document", map[string]any{"id": "0"}},
		{"researchguy_source", map[string]any{"ref": "https://nowhere.example/"}},
	} {
		if res := callOn(t, c, bad.tool, bad.args); !res.IsError {
			t.Errorf("%s %v succeeded: %s", bad.tool, bad.args, extractText(t, res))
		}
	}
	if _, err := c.CallTool(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "researchguy_ingest_url", Arguments: map[string]any{"url": srv.URL + "/survey"}}}); err == nil {
		t.Error("the read profile ingested a URL")
	}
	if res := callOn(t, full, "researchguy_ingest_url", map[string]any{"url": "ftp://x.example/a"}); !res.IsError {
		t.Errorf("ftp ingest succeeded")
	}
}
