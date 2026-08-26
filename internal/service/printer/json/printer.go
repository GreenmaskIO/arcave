// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
