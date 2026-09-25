package graph

import (
	"fmt"
	"sync"
	"testing"
)

// TestNewStore_ConcurrentWriters simulates parallel `researchguy graph
// add-node` processes: each writer opens its own Store (its own connection
// pool) on the same tasks.db and inserts nodes at the same time.
func TestNewStore_ConcurrentWriters(t *testing.T) {
	t.Setenv("RESEARCHGUY_CONFIG_DIR", t.TempDir())

	const writers, perWriter = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*(perWriter+1))
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			s, err := NewStore(nil)
			if err != nil {
				errs <- fmt.Errorf("writer %d: NewStore: %w", w, err)
				return
			}
			defer s.Close()
			for i := 0; i < perWriter; i++ {
				n := &Node{Type: NodeClaim, Title: fmt.Sprintf("claim %d-%d", w, i)}
				if err := s.CreateNode(n); err != nil {
					errs <- fmt.Errorf("writer %d node %d: %w", w, i, err)
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	s, err := NewStore(nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()
	nodes, err := s.ListNodes(NodeClaim)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != writers*perWriter {
		t.Errorf("stored %d nodes, want %d", len(nodes), writers*perWriter)
	}
}
