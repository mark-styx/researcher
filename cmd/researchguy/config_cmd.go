package main

import (
	"fmt"
	"os"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "config",
		Short:   "Manage researchguy configuration",
		Long:    `View and update researchguy configuration. Config is stored in ~/.researchguy/config.yaml.`,
		GroupID: "setup",
	}

	cmd.AddCommand(configShowCmd())
	cmd.AddCommand(configSetCmd())
	return cmd
}

func configShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "show",
		Short:   "Print current configuration",
		Example: `  researchguy config show`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			data, err := yaml.Marshal(cfg)
			if err != nil {
				return fmt.Errorf("marshaling config: %w", err)
			}

			fmt.Print(string(data))
			return nil
		},
	}
}

func configSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Update a config value",
		Long: `Update a top-level configuration key. For nested keys, use the top-level
YAML key name. The value is written directly to ~/.researchguy/config.yaml.`,
		Example: `  researchguy config set research_dir ~/my-research
  researchguy config set default_backend ollama`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]

			configPath := config.FilePath()
			data, err := os.ReadFile(configPath)
			if err != nil {
				return fmt.Errorf("reading config: %w", err)
			}

			// Parse as generic map for flexible key setting
			var raw map[string]interface{}
			if err := yaml.Unmarshal(data, &raw); err != nil {
				return fmt.Errorf("parsing config: %w", err)
			}

			raw[key] = value

			out, err := yaml.Marshal(raw)
			if err != nil {
				return fmt.Errorf("marshaling config: %w", err)
			}

			if err := os.WriteFile(configPath, out, 0644); err != nil {
				return fmt.Errorf("writing config: %w", err)
			}

			fmt.Printf("Set %s = %s\n", key, value)
			return nil
		},
	}
}
