package research

import "testing"

func TestSlugify(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "Quantum Computing", "quantum-computing"},
		{"special chars", "AI & Machine Learning!", "ai-machine-learning"},
		{"extra spaces", "  hello   world  ", "hello-world"},
		{"numbers", "GPT-4 vs Claude 3", "gpt-4-vs-claude-3"},
		{"all special", "!@#$%", "unnamed"},
		{"empty", "", "unnamed"},
		{"single word", "rust", "rust"},
		{"trailing hyphens", "---test---", "test"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Slugify(tc.input)
			if got != tc.want {
				t.Errorf("Slugify(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
