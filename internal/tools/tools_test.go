package tools

import "testing"

func TestDefaultTools(t *testing.T) {
	tt := DefaultTools()

	if len(tt) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tt))
	}

	expected := []string{"web_search", "web_fetch"}
	for i, name := range expected {
		if tt[i].Name != name {
			t.Errorf("tool[%d] name = %q, want %q", i, tt[i].Name, name)
		}
		if tt[i].Description == "" {
			t.Errorf("tool[%d] description is empty", i)
		}

		params := tt[i].Parameters
		if _, ok := params["type"]; !ok {
			t.Errorf("tool[%d] parameters missing 'type'", i)
		}
		if _, ok := params["properties"]; !ok {
			t.Errorf("tool[%d] parameters missing 'properties'", i)
		}
		if _, ok := params["required"]; !ok {
			t.Errorf("tool[%d] parameters missing 'required'", i)
		}
	}
}
