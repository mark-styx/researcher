package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot get home dir: %v", err)
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "tilde prefix",
			input: "~/foo",
			want:  filepath.Join(home, "foo"),
		},
		{
			name:  "absolute path unchanged",
			input: "/usr/local/bin",
			want:  "/usr/local/bin",
		},
		{
			name:  "relative path unchanged",
			input: "relative/path",
			want:  "relative/path",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpandPath(tc.input)
			if got != tc.want {
				t.Errorf("ExpandPath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestDefaultYAML_Parses(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte(DefaultYAML), &cfg); err != nil {
		t.Fatalf("failed to unmarshal DefaultYAML: %v", err)
	}

	if !cfg.Tools.Enabled {
		t.Error("expected Tools.Enabled = true")
	}
	if cfg.Claude.Model != "opus" {
		t.Errorf("Claude.Model = %q, want %q", cfg.Claude.Model, "opus")
	}
	if cfg.DefaultBackend != "claude" {
		t.Errorf("DefaultBackend = %q, want %q", cfg.DefaultBackend, "claude")
	}
	if cfg.Ollama.Host != "http://localhost:11434" {
		t.Errorf("Ollama.Host = %q, want %q", cfg.Ollama.Host, "http://localhost:11434")
	}
	if cfg.Tools.MaxResults != 10 {
		t.Errorf("Tools.MaxResults = %d, want 10", cfg.Tools.MaxResults)
	}
	if cfg.Tools.MaxIterations != 20 {
		t.Errorf("Tools.MaxIterations = %d, want 20", cfg.Tools.MaxIterations)
	}
}
