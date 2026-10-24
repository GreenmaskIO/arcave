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
	"encoding/json"
	"fmt"
	"time"

	"github.com/greenmaskio/arcave/internal/model"
)

// RunnerInfo identifies the process an operation runs in.
type RunnerInfo struct {
	Host          string `json:"host"`
	PID           int    `json:"pid"`
	ArcaveVersion string `json:"arcave_version"`
}

// Heartbeat is what an operation writing a tree reports about itself. The tree's record, when
// there is one, outranks it. Nothing may be decided from Data, which only the operation can read.
type Heartbeat struct {
	Operation  model.HeartbeatOperation `json:"operation"`
	Status     Status                   `json:"status"`
	RunnerInfo RunnerInfo               `json:"runner_info"`
	StartedAt  time.Time                `json:"started_at"`
	UpdatedAt  time.Time                `json:"updated_at"`
	FinishedAt time.Time                `json:"finished_at,omitzero"`
	Error      string                   `json:"error,omitempty"`
	Data       json.RawMessage          `json:"data,omitempty"`
}

// StatusAt is the status the heartbeat stands for at now: an in-progress one is failed once
// lastModified, the store's clock and never UpdatedAt, is staleAfter old.
func (h Heartbeat) StatusAt(lastModified, now time.Time, staleAfter time.Duration) (Status, error) {
	if err := h.Status.Validate(); err != nil {
		return "", err
	}
	if h.Status == StatusInProgress && now.Sub(lastModified) >= staleAfter {
		return StatusFailed, nil
	}
	return h.Status, nil
}

// EncodeData renders an operation's Data.
func EncodeData(data any) (json.RawMessage, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode heartbeat data: %w", err)
	}
	return raw, nil
}

// DecodeData reads a heartbeat's Data as T, the type its Operation names.
func DecodeData[T any](h Heartbeat) (T, error) {
	var data T
	if err := json.Unmarshal(h.Data, &data); err != nil {
		return data, fmt.Errorf("decode %s heartbeat data: %w", h.Operation, err)
	}
	return data, nil
}
