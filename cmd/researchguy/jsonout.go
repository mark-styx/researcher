package main

import (
	"encoding/json"
	"os"
)

// printJSON writes v to stdout as indented JSON. Commands run with --json
// print nothing else on stdout; progress and warnings go to stderr.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
