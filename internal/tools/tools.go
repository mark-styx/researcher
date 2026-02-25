package tools

// Tool describes a function the LLM can call.
type Tool struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ToolCall represents a tool invocation requested by the LLM.
type ToolCall struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// ToolResult holds the output of executing a tool.
type ToolResult struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

// DefaultTools returns the standard tool set for research tasks.
func DefaultTools() []Tool {
	return []Tool{
		{
			Name:        "web_search",
			Description: "Search the web for current information using DuckDuckGo. Returns a list of search results with titles, URLs, and snippets.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "The search query",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "web_fetch",
			Description: "Fetch the text content of a web page. Returns the extracted text from the given URL.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"url": map[string]interface{}{
						"type":        "string",
						"description": "The URL to fetch",
					},
				},
				"required": []string{"url"},
			},
		},
	}
}
