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

// Package reporter holds progress.Reporters that render a stream as JSON lines or log records.
package reporter

import (
	"encoding/json"
	"io"
	"sync"

	"github.com/greenmaskio/arcave/pkg/progress"
)

// JSON writes the stream as is, one event per line.
type JSON struct {
	mu  sync.Mutex
	enc *json.Encoder
	err error
}

// NewJSON returns a JSON reporter writing to w.
func NewJSON(w io.Writer) *JSON {
	return &JSON{enc: json.NewEncoder(w)}
}

// Report writes evt. After a failed write it writes nothing more: the stream is torn.
func (r *JSON) Report(evt progress.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	r.err = r.enc.Encode(evt)
}

// Err returns the write that tore the stream, if any.
func (r *JSON) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}
