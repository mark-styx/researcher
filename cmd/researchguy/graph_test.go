package main

import (
	"testing"

	"github.com/marklubin/researchguy/internal/graph"
)

func TestNodeFlags(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]any
		want string
	}{
		{"no flags", nil, ""},
		{"needs review only", map[string]any{"needs_review": true}, "REVIEW"},
		{"orphaned only", map[string]any{"orphaned": true}, "ORPHANED"},
		{"both", map[string]any{"needs_review": true, "orphaned": true}, "REVIEW,ORPHANED"},
		{"false values ignored", map[string]any{"needs_review": false, "orphaned": false}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := &graph.Node{Metadata: tc.meta}
			if got := nodeFlags(n); got != tc.want {
				t.Errorf("nodeFlags() = %q, want %q", got, tc.want)
			}
		})
	}
}
