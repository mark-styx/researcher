package main

import (
	"context"
	"fmt"
	"os"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/critique"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/spf13/cobra"
)

// critiqueOutput is `researchguy critique --json`.
type critiqueOutput struct {
	Text          string   `json:"text"`
	Evidence      []string `json:"evidence"`
	EvidenceChars int      `json:"evidence_chars"`
	Truncated     bool     `json:"truncated"`
	Skipped       []string `json:"skipped,omitempty"`
	Backend       string   `json:"backend"`
	*critique.Result
}

func critiqueCmd() *cobra.Command {
	var textPath, backend, model string
	var evidence []string
	var maxChars int
	var jsonOut bool

	cmd := &cobra.Command{
		Use:     "critique",
		Short:   "Flag claims in a text that its evidence doesn't support",
		GroupID: "research",
		Long: `Runs the hybrid backend's two critics against any text and evidence files,
in flag mode: nothing is rewritten.

  groundedness  factual claims the evidence does not support (not found in
                the evidence given, which is not the same as false)
  narrative     claims stated as settled without a distinct piece of evidence

Evidence files are read in order up to --max-evidence-chars; the file that
crosses the limit is cut and later ones are skipped (reported in --json).`,
		Example: `  researchguy critique --text chapters/01/final.md --evidence research/web/001-researcher-1.md --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			text, err := os.ReadFile(textPath)
			if err != nil {
				return fmt.Errorf("reading text: %w", err)
			}
			loaded, err := critique.LoadEvidence(evidence, maxChars)
			if err != nil {
				return err
			}
			provider, err := llm.NewProvider(cfg, backend, model)
			if err != nil {
				return fmt.Errorf("creating provider: %w", err)
			}
			if !jsonOut {
				fmt.Fprintf(os.Stderr, "Critiquing %s against %d evidence file(s) (%d chars) with %s...\n", textPath, len(loaded.Evidence), loaded.Chars, provider.Name())
			}
			res, err := critique.Run(cmd.Context(), providerCompleter(provider), string(text), loaded.Evidence)
			if err != nil {
				return err
			}
			if jsonOut {
				out := critiqueOutput{
					Text:          textPath,
					EvidenceChars: loaded.Chars,
					Truncated:     loaded.Truncated,
					Skipped:       loaded.Skipped,
					Backend:       provider.Name(),
					Result:        res,
				}
				for _, e := range loaded.Evidence {
					out.Evidence = append(out.Evidence, e.Label)
				}
				return printJSON(out)
			}
			fmt.Print(res.Markdown())
			return nil
		},
	}

	cmd.Flags().StringVar(&textPath, "text", "", "File holding the text to critique (required)")
	cmd.Flags().StringSliceVar(&evidence, "evidence", nil, "Evidence file (repeatable, required)")
	cmd.Flags().IntVar(&maxChars, "max-evidence-chars", critique.DefaultMaxEvidenceChars, "Cap on evidence sent to the critics")
	cmd.Flags().StringVar(&backend, "backend", "", "LLM backend (default: default_backend from config)")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print JSON: {text, evidence, evidence_chars, truncated, backend, groundedness, narrative, flags}")
	cmd.MarkFlagRequired("text")
	cmd.MarkFlagRequired("evidence")
	return cmd
}

// providerCompleter adapts an llm.Provider to critique's Completer.
func providerCompleter(p llm.Provider) critique.Completer {
	return func(ctx context.Context, system, user string) (string, error) {
		return p.Complete(ctx, llm.Request{SystemPrompt: system, UserPrompt: user})
	}
}
