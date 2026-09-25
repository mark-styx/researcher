package research

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/marklubin/researchguy/internal/llm"
)

// metaProvider sets its metadata during Complete, like the hybrid backend.
type metaProvider struct {
	mockProvider
	last string
}

func (m *metaProvider) Complete(ctx context.Context, req llm.Request) (string, error) {
	out, err := m.mockProvider.Complete(ctx, req)
	m.last = `{"call":` + string(rune('0'+len(m.calls))) + `}`
	return out, err
}

func (m *metaProvider) Metadata() string { return m.last }

func TestRunner_MetadataComesFromTheReportCall(t *testing.T) {
	for _, typ := range []string{TypeDive, TypeReview, TypeCompare, TypeWatch, TypeAsk} {
		t.Run(typ, func(t *testing.T) {
			cfg := testConfig(t)
			p := &metaProvider{mockProvider: mockProvider{response: "body"}}
			res, err := NewRunner(cfg, p).Run(context.Background(), Task{
				Type: typ, Topic: "t", NoResearch: true, Quiet: true,
				OutPath: filepath.Join(t.TempDir(), "o.md"),
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.Metadata != `{"call":1}` {
				t.Fatalf("metadata = %q, want the report call's metadata", res.Metadata)
			}
		})
	}
}
