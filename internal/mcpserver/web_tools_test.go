package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/marklubin/researchguy/internal/tools"
)

// The web profile is only web_search and web_fetch: a Goose worker gets the
// web and nothing of the store, the graph or research.
func TestWebProfile_OnlyWebTools(t *testing.T) {
	c := profileClient(t, NewWeb(testConfig(t), "test"))
	res, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := slices.Clone(WebTools)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("web profile tools = %v, want %v", names, want)
	}
}

func fakeWeb(t *testing.T, search searchFunc, fetch fetchFunc) *client.Client {
	t.Helper()
	return profileClient(t, newWeb("test", search, fetch))
}

func TestWebSearch_StructuredResultsAndText(t *testing.T) {
	var gotQuery string
	c := fakeWeb(t, func(_ context.Context, q string) ([]tools.SearchResult, error) {
		gotQuery = q
		return []tools.SearchResult{
			{Title: "CNN transcript", URL: "https://transcripts.cnn.com/show/acd/date/2021-09-01/segment/01", Snippet: "Rogan said he took ivermectin"},
			{Title: "FDA EUA", URL: "https://www.fda.gov/eua", Snippet: ""},
		}, nil
	}, nil)

	res := callOn(t, c, "web_search", map[string]any{"query": "rogan ivermectin cnn"})
	if res.IsError {
		t.Fatalf("web_search error: %s", extractText(t, res))
	}
	if gotQuery != "rogan ivermectin cnn" {
		t.Errorf("search got query %q", gotQuery)
	}
	text := extractText(t, res)
	if !strings.Contains(text, "1. **CNN transcript**") || !strings.Contains(text, "URL: https://www.fda.gov/eua") {
		t.Errorf("fallback text = %q, want the formatted result list", text)
	}

	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var structured struct {
		Results []webSearchResult `json:"results"`
	}
	if err := json.Unmarshal(b, &structured); err != nil {
		t.Fatalf("structured content %s: %v", b, err)
	}
	if len(structured.Results) != 2 || structured.Results[0].Rank != 1 || structured.Results[1].Rank != 2 {
		t.Fatalf("structured results = %+v, want two ranked results", structured.Results)
	}
	if structured.Results[0].Snippet != "Rogan said he took ivermectin" || structured.Results[1].URL != "https://www.fda.gov/eua" {
		t.Errorf("structured results = %+v", structured.Results)
	}
}

func TestWebSearch_Errors(t *testing.T) {
	c := fakeWeb(t, func(context.Context, string) ([]tools.SearchResult, error) {
		return nil, errors.New("rate limited")
	}, nil)
	if res := callOn(t, c, "web_search", map[string]any{}); !res.IsError {
		t.Errorf("web_search without a query should be an error result")
	}
	res := callOn(t, c, "web_search", map[string]any{"query": "x"})
	if !res.IsError || !strings.Contains(extractText(t, res), "search failed: rate limited") {
		t.Errorf("failed search = %s", extractText(t, res))
	}
}

func TestWebFetch_TextAndErrors(t *testing.T) {
	c := fakeWeb(t, nil, func(_ context.Context, url string) (string, error) {
		if url == "https://bad.example/" {
			return "", errors.New("status 403")
		}
		return "page text for " + url, nil
	})

	res := callOn(t, c, "web_fetch", map[string]any{"url": "https://ok.example/"})
	if res.IsError || extractText(t, res) != "page text for https://ok.example/" {
		t.Errorf("web_fetch = %s", extractText(t, res))
	}
	res = callOn(t, c, "web_fetch", map[string]any{"url": "https://bad.example/"})
	if !res.IsError || !strings.Contains(extractText(t, res), "fetch failed: status 403") {
		t.Errorf("failed fetch = %s", extractText(t, res))
	}
	if res := callOn(t, c, "web_fetch", map[string]any{}); !res.IsError {
		t.Errorf("web_fetch without a url should be an error result")
	}
}
