package main

import (
	"fmt"

	"github.com/mark3labs/mcp-go/server"
	"github.com/marklubin/researcher/internal/config"
	"github.com/marklubin/researcher/internal/llm"
	"github.com/marklubin/researcher/internal/mcpserver"
	"github.com/spf13/cobra"
)

func mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Start MCP server (stdio transport)",
		Long: `Start a Model Context Protocol (MCP) server using stdio transport.
This exposes researcher's capabilities as tools that any MCP client can call.

Tools provided:
  researcher_ask      Ask a question with research context
  researcher_dive     Deep-dive research report
  researcher_review   Literature review / synthesis
  researcher_search   Semantic search across research
  researcher_list     List research files by category
  researcher_read     Read a research document

Configure in Claude Code settings:
  {
    "mcpServers": {
      "researcher": {
        "command": "researcher",
        "args": ["mcp"]
      }
    }
  }`,
		Example: `  researcher mcp`,
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
