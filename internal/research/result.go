package research

import "encoding/json"

// Fields returns the result as a JSON-ready map shared by the CLI --json
// output and the MCP tools: {key: response, "saved_to", "backend",
// "run_id" and "run_dir" when the task has a run record, and "metadata"
// when the provider reported any}. Metadata that is valid JSON is
// embedded as-is.
func (r RunResult) Fields(key, backend string) map[string]any {
	out := map[string]any{key: r.Response, "saved_to": r.FilePath, "backend": backend}
	if r.RunID != "" {
		out["run_id"] = r.RunID
		out["run_dir"] = r.RunDir
	}
	if r.Metadata != "" {
		if json.Valid([]byte(r.Metadata)) {
			out["metadata"] = json.RawMessage(r.Metadata)
		} else {
			out["metadata"] = r.Metadata
		}
	}
	return out
}
