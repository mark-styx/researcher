package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// printJSON writes v to stdout as indented JSON. Commands run with --json
// print nothing else on stdout; progress and warnings go to stderr.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// printRunRecord tells the user where a task's run record (its captured
// evidence, worker drafts and metadata) was written.
func printRunRecord(w io.Writer, dir string) {
	if dir != "" {
		fmt.Fprintf(w, "Run record: %s\n", dir)
	}
}
