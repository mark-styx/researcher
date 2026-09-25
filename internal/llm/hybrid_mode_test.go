package llm

import "testing"

func TestIsValidMode(t *testing.T) {
	for mode, want := range map[string]bool{"": true, "landscape": true, "inquiry": true, "Inquiry": false, "deep": false} {
		if got := IsValidMode(mode); got != want {
			t.Errorf("IsValidMode(%q) = %v, want %v", mode, got, want)
		}
	}
}
