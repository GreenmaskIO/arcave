package cmd

import (
	"encoding/json"
	"io"
)

// writeJSON renders v as indented JSON to w. All gm commands report their
// results as JSON on stdout.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
