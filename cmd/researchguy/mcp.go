package main

import (
	"fmt"

	"github.com/mark3labs/mcp-go/server"
	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/mcpserver"
	"github.com/spf13/cobra"
)

func mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Start MCP server (stdio transport)",
		Long: `Start a Model Context Protocol (MCP) server using stdio transport.
This exposes researchguy's capabilities as tools that any MCP client can call.

Tools provided:
  researchguy_ask             Ask a question with research context
  researchguy_dive            Deep-dive research report (backend/mode/branches/projects)
  researchguy_review          Literature review / synthesis
  researchguy_compare         Side-by-side comparison
  researchguy_enrich          Expand an existing document
  researchguy_search          Semantic search across research (projects)
  researchguy_context         Existing research on a topic, no LLM call (projects)
  researchguy_list            List research files by category
  researchguy_read            Read a research document or a read_roots file
  researchguy_graph_list      List knowledge-graph nodes
  researchguy_graph_show      One node with its edges
  researchguy_graph_find      Find nodes by title, path, or metadata value
  researchguy_graph_add_node  Create a node
  researchguy_graph_add_edge  Create an edge

Configure in Claude Code settings:
  {
    "mcpServers": {
      "researchguy": {
        "command": "researchguy",
        "args": ["mcp"]
      }
    }
  }`,
		Example: `  researchguy mcp`,
		GroupID: "setup",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			provider, err := llm.NewProvider(cfg, "", "")
			if err != nil {
				return fmt.Errorf("creating LLM provider: %w", err)
			}

			s := mcpserver.New(cfg, provider, Version)
			return server.ServeStdio(s)
		},
	}
}
