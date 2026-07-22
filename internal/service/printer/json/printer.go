package json

import (
	"encoding/json"
	"io"
)

// Printer renders values as indented JSON to w.
type Printer struct {
	w io.Writer
}

// New returns a Printer that writes indented JSON to w. All arcave
// commands report their results as JSON; logs go to stderr via the context
// logger.
func New(w io.Writer) *Printer {
	return &Printer{w: w}
}

func (p *Printer) Print(v any) error {
	enc := json.NewEncoder(p.w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
