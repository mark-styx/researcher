package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/graph"
	"github.com/spf13/cobra"
)

func graphCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Manage the node/edge knowledge graph",
		Long: `Entities, sources, claims, funding-pattern observations, and reports
persist as independently referenceable nodes connected by typed edges,
instead of being re-derived or restated inside every report that touches
them. Node structure and relationships live in ~/.researchguy/tasks.db;
node content lives in markdown files under the research directory.`,
		GroupID: "graph",
	}

	cmd.AddCommand(graphAddNodeCmd())
	cmd.AddCommand(graphAddEdgeCmd())
	cmd.AddCommand(graphShowCmd())
	cmd.AddCommand(graphListCmd())
	cmd.AddCommand(graphExportCmd())
	cmd.AddCommand(graphApproveCmd())
	return cmd
}

// nodeFlags renders a node's rollup-related metadata flags for display in
// "graph list"/"graph show": REVIEW for a pending rollup resummarization,
// ORPHANED for a node whose linked file no longer exists.
func nodeFlags(n *graph.Node) string {
	var flags []string
	if needsReview, _ := n.Metadata["needs_review"].(bool); needsReview {
		flags = append(flags, "REVIEW")
	}
	if orphaned, _ := n.Metadata["orphaned"].(bool); orphaned {
		flags = append(flags, "ORPHANED")
	}
	return strings.Join(flags, ",")
}

func openGraphStore() (*config.Config, *graph.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("loading config: %w", err)
	}
	store, err := graph.NewStore(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("opening graph store: %w", err)
	}
	return cfg, store, nil
}

func graphAddNodeCmd() *cobra.Command {
	var nodeType, title, path, summary string

	cmd := &cobra.Command{
		Use:   "add-node",
		Short: "Create a node (entity, source, claim, funding-pattern, or report)",
		Example: `  researchguy graph add-node --type entity --title "Acme Research Institute"
  researchguy graph add-node --type funding-pattern --title "Acme funds Study X" \
    --summary "Acme sponsored Study X, which reached conclusions aligned with Acme's stated position" \
    --path funding/acme-study-x.md`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, store, err := openGraphStore()
			if err != nil {
				return err
			}
			defer store.Close()

			n := &graph.Node{Type: nodeType, Title: title, Path: path, Summary: summary}
			if err := store.CreateNode(n); err != nil {
				return fmt.Errorf("creating node: %w", err)
			}
			fmt.Println(n.ID)
			return nil
		},
	}

	types := fmt.Sprintf("%v", graph.ValidNodeTypes)
	cmd.Flags().StringVar(&nodeType, "type", "", "Node type "+types+" (required)")
	cmd.Flags().StringVar(&title, "title", "", "Node title (required)")
	cmd.Flags().StringVar(&path, "path", "", "Path to the node's markdown content, relative to research_dir")
	cmd.Flags().StringVar(&summary, "summary", "", "Short summary shown in listings and references")
	cmd.MarkFlagRequired("type")
	cmd.MarkFlagRequired("title")
	return cmd
}

func graphAddEdgeCmd() *cobra.Command {
	var from, to, edgeType string

	cmd := &cobra.Command{
		Use:   "add-edge",
		Short: "Create a typed edge between two existing nodes",
		Example: `  researchguy graph add-edge --from <claim-id> --to <source-id> --type supports
  researchguy graph add-edge --from <org-id> --to <study-id> --type sponsors-research`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, store, err := openGraphStore()
			if err != nil {
				return err
			}
			defer store.Close()

			e := &graph.Edge{FromID: from, ToID: to, Type: edgeType}
			if err := store.CreateEdge(e); err != nil {
				return fmt.Errorf("creating edge: %w", err)
			}
			fmt.Println(e.ID)
			return nil
		},
	}

	types := fmt.Sprintf("%v", graph.ValidEdgeTypes)
	cmd.Flags().StringVar(&from, "from", "", "Source node ID (required)")
	cmd.Flags().StringVar(&to, "to", "", "Target node ID (required)")
	cmd.Flags().StringVar(&edgeType, "type", "", "Edge type "+types+" (required)")
	cmd.MarkFlagRequired("from")
	cmd.MarkFlagRequired("to")
	cmd.MarkFlagRequired("type")
	return cmd
}

func graphShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "show <node-id>",
		Short:   "Show a node's details and its connected edges",
		Args:    cobra.ExactArgs(1),
		Example: `  researchguy graph show 3f9c2e1a-...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, store, err := openGraphStore()
			if err != nil {
				return err
			}
			defer store.Close()

			id := args[0]
			n, err := store.GetNode(id)
			if err != nil {
				return fmt.Errorf("node not found: %w", err)
			}

			fmt.Printf("ID:      %s\n", n.ID)
			fmt.Printf("Type:    %s\n", n.Type)
			fmt.Printf("Title:   %s\n", n.Title)
			if n.Path != "" {
				fmt.Printf("Path:    %s\n", n.Path)
			}
			if n.Summary != "" {
				fmt.Printf("Summary: %s\n", n.Summary)
			}
			fmt.Printf("Updated: %s\n", n.UpdatedAt.Format("2006-01-02 15:04"))

			if orphaned, _ := n.Metadata["orphaned"].(bool); orphaned {
				fmt.Printf("\n[ORPHANED] Linked file %q no longer exists.\n", n.Path)
			}
			if needsReview, _ := n.Metadata["needs_review"].(bool); needsReview {
				fmt.Println("\n[PENDING REVIEW] Rollup proposed an updated summary:")
				if pending, ok := n.Metadata["pending_summary"].(string); ok {
					fmt.Printf("  %s\n", pending)
				}
				fmt.Printf("  Run: researchguy graph approve %s\n", n.ID)
			}

			out, err := store.ListEdges(graph.EdgeFilter{FromID: id})
			if err != nil {
				return fmt.Errorf("listing outgoing edges: %w", err)
			}
			in, err := store.ListEdges(graph.EdgeFilter{ToID: id})
			if err != nil {
				return fmt.Errorf("listing incoming edges: %w", err)
			}

			if len(out) > 0 {
				fmt.Println("\nOutgoing:")
				for _, e := range out {
					target, terr := store.GetNode(e.ToID)
					label := e.ToID
					if terr == nil {
						label = target.Title
					}
					fmt.Printf("  --%s--> %s (%s)\n", e.Type, label, e.ToID)
				}
			}
			if len(in) > 0 {
				fmt.Println("\nIncoming:")
				for _, e := range in {
					source, serr := store.GetNode(e.FromID)
					label := e.FromID
					if serr == nil {
						label = source.Title
					}
					fmt.Printf("  %s --%s--> (this)\n", label, e.Type)
				}
			}
			return nil
		},
	}
}

func graphListCmd() *cobra.Command {
	var typeFilter string

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List nodes, optionally filtered by type",
		Example: `  researchguy graph list --type entity`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, store, err := openGraphStore()
			if err != nil {
				return err
			}
			defer store.Close()

			nodes, err := store.ListNodes(typeFilter)
			if err != nil {
				return fmt.Errorf("listing nodes: %w", err)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tTYPE\tTITLE\tUPDATED\tFLAGS")
			for _, n := range nodes {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					n.ID[:8], n.Type, n.Title, n.UpdatedAt.Format("2006-01-02"), nodeFlags(n))
			}
			w.Flush()
			return nil
		},
	}

	cmd.Flags().StringVar(&typeFilter, "type", "", "Filter by node type")
	return cmd
}

// graphExport is the JSON shape written by `graph export`: everything a
// viewer needs to render nodes and typed edges without further lookups.
type graphExport struct {
	Nodes []*graph.Node `json:"nodes"`
	Edges []*graph.Edge `json:"edges"`
}

func graphExportCmd() *cobra.Command {
	var outPath string

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the full graph as JSON (nodes + edges) for visualization",
		Example: `  researchguy graph export
  researchguy graph export --out graph.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, store, err := openGraphStore()
			if err != nil {
				return err
			}
			defer store.Close()

			nodes, err := store.ListNodes("")
			if err != nil {
				return fmt.Errorf("listing nodes: %w", err)
			}
			edges, err := store.ListEdges(graph.EdgeFilter{})
			if err != nil {
				return fmt.Errorf("listing edges: %w", err)
			}

			b, err := json.MarshalIndent(graphExport{Nodes: nodes, Edges: edges}, "", "  ")
			if err != nil {
				return fmt.Errorf("encoding graph: %w", err)
			}

			if outPath == "" {
				fmt.Println(string(b))
				return nil
			}
			if err := os.WriteFile(outPath, b, 0644); err != nil {
				return fmt.Errorf("writing %s: %w", outPath, err)
			}
			fmt.Printf("Graph exported to: %s\n", outPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&outPath, "out", "", "Write JSON to this path instead of stdout")
	return cmd
}

func graphApproveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "approve <node-id>",
		Short: "Approve a pending rollup resummarization",
		Long: `The background rollup pass (see "researchguy daemon start" and the
graph.rollup config section) never overwrites a node's Summary directly.
Instead it writes a proposed resummarization to the node's metadata and
flags it REVIEW. "graph approve" copies that proposal into Summary, clears
the flag, and bumps updated_at. Use "graph show <id>" first to see what
changed.`,
		Args:    cobra.ExactArgs(1),
		Example: `  researchguy graph approve 3f9c2e1a-...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, store, err := openGraphStore()
			if err != nil {
				return err
			}
			defer store.Close()

			id := args[0]
			n, err := store.GetNode(id)
			if err != nil {
				return fmt.Errorf("node not found: %w", err)
			}

			pending, _ := n.Metadata["pending_summary"].(string)
			if pending == "" {
				return fmt.Errorf("node %s has no pending_summary to approve", id)
			}

			n.Summary = pending
			delete(n.Metadata, "pending_summary")
			delete(n.Metadata, "needs_review")
			if err := store.UpdateNode(n); err != nil {
				return fmt.Errorf("approving node: %w", err)
			}
			fmt.Printf("Approved: %s\n", n.ID)
			return nil
		},
	}
}
