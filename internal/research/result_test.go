package research

import (
	"encoding/json"
	"testing"
)

func TestRunResultFields(t *testing.T) {
	f := RunResult{FilePath: "/x.md", Response: "body", Metadata: `{"shards":2}`}.Fields("report", "hybrid")
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"backend":"hybrid","metadata":{"shards":2},"report":"body","saved_to":"/x.md"}` {
		t.Fatalf("got %s", data)
	}
	if f := (RunResult{Metadata: "not json"}).Fields("answer", "claude"); f["metadata"] != "not json" {
		t.Fatalf("raw metadata lost: %v", f)
	}
	if _, ok := (RunResult{}).Fields("answer", "claude")["metadata"]; ok {
		t.Fatal("empty metadata should be omitted")
	}
}
