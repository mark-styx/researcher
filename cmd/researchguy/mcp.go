package main

import (
	"fmt"

	"github.com/mark3labs/mcp-go/server"
	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/mcpserver"
	"github.com/spf13/cobra"
)

// MCP server profiles.
const (
	profileFull = "full"
	profileRead = "read"
)

func mcpCmd() *cobra.Command {
	var profile string
	cmd := &cobra.Command{
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
  researchguy_critique        Check a report's claims against its evidence
  researchguy_search          Semantic search across research (projects)
  researchguy_context         Existing research on a topic, no LLM call (projects)
  researchguy_list            List research files by category
  researchguy_read            Read a research document or a read_roots file
  researchguy_find            Search fetched documents and reports, with dates (store.dsn)
  researchguy_passage         One passage with the passages around it
  researchguy_document        A fetched document's text, versions and fetches
  researchguy_source          A source's documents, sightings, fetches, citing runs
  researchguy_ingest_url      Fetch a URL into the store and index now
  researchguy_graph_list      List knowledge-graph nodes
  researchguy_graph_show      One node with its edges
  researchguy_graph_find      Find nodes by title, path, or metadata value
  researchguy_graph_add_node  Create a node
  researchguy_graph_add_edge  Create an edge

--profile read serves only the tools that read: search, context, list,
read, find, passage, document, source and the graph's list, show and find.
No LLM calls, no fetching, no graph writes. The hybrid aggregator gets this
profile while it writes a report.

Configure in Claude Code settings:
  {
    "mcpServers": {
      "researchguy": {
        "command": "researchguy",
        "args": ["mcp"]
      }
    }
  }`,
		Example: `  researchguy mcp
  researchguy mcp --profile read`,
		GroupID: "setup",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if profile != profileFull && profile != profileRead {
				return fmt.Errorf("unknown --profile %q (want %s or %s)", profile, profileFull, profileRead)
			}
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			if profile == profileRead {
				return server.ServeStdio(mcpserver.NewRead(cfg, Version))
			}

			provider, err := llm.NewProvider(cfg, "", "")
			if err != nil {
				return fmt.Errorf("creating LLM provider: %w", err)
			}

			s := mcpserver.New(cfg, provider, Version)
			return server.ServeStdio(s)
		},
	}
	cmd.Flags().StringVar(&profile, "profile", profileFull, "Tools to serve: full, or read (lookups only)")
	return cmd
}
