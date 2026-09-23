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
  researchguy_ask      Ask a question with research context
  researchguy_dive     Deep-dive research report
  researchguy_review   Literature review / synthesis
  researchguy_search   Semantic search across research
  researchguy_list     List research files by category
  researchguy_read     Read a research document

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
