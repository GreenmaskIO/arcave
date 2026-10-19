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

package heartbeat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/greenmaskio/storages"
)

// FileName is the heartbeat's key at the root of the tree it reports on.
const FileName = "heartbeat.json"

// Writer stores a heartbeat at the root of one tree's store, replacing the one there.
type Writer struct {
	st storages.Writer
}

// NewWriter returns a writer into st, the store of the tree the heartbeat reports on.
func NewWriter(st storages.Writer) *Writer {
	return &Writer{st: st}
}

// Write validates and stores h.
func (s *Writer) Write(ctx context.Context, beat Heartbeat) error {
	if err := beat.Status.Validate(); err != nil {
		return fmt.Errorf("write heartbeat: %w", err)
	}
	data, err := json.Marshal(beat)
	if err != nil {
		return fmt.Errorf("encode heartbeat: %w", err)
	}
	if err := s.st.PutObject(ctx, FileName, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("write heartbeat: %w", err)
	}
	return nil
}
