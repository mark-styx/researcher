package main

import (
	"fmt"
	"os"

	"github.com/marklubin/researcher/internal/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage researcher configuration",
	}

	cmd.AddCommand(configShowCmd())
	cmd.AddCommand(configSetCmd())
	return cmd
}

func configShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print current configuration",
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
		Args:  cobra.ExactArgs(2),
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
