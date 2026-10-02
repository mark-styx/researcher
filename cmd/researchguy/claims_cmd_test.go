package main

import (
	"testing"

	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/retrieve"
)

func TestRelatedLine(t *testing.T) {
	base := retrieve.Related{Ref: "C:7", Domain: "zoo.example", Dated: "2024-06-01", Text: "Dogs are the popular animal."}
	for _, tc := range []struct {
		name string
		edit func(*retrieve.Related)
		want string
	}{
		{"model", func(r *retrieve.Related) {
			r.Relation, r.Method, r.Model, r.Confidence, r.Note = retrieve.RelContradicts, store.LinkModel, "claude/sonnet", 0.7, "dogs, not cats"
		}, "contradicts [C:7] (zoo.example, 2024-06-01; model-labeled by claude/sonnet, 0.70): Dogs are the popular animal. (dogs, not cats)"},
		{"rule", func(r *retrieve.Related) { r.Relation, r.Method = retrieve.RelSame, store.LinkRule },
			"same [C:7] (zoo.example, 2024-06-01; same quoted words): Dogs are the popular animal."},
		{"human", func(r *retrieve.Related) { r.Relation, r.Method = retrieve.RelSupersededBy, store.LinkHuman },
			"superseded by [C:7] (zoo.example, 2024-06-01; set by hand): Dogs are the popular animal."},
	} {
		r := base
		tc.edit(&r)
		if got := relatedLine(r); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
