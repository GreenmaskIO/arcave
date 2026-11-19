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
package test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// The part of the compose provider's body of a savepoint these tests read. The body is the
// provider's own, so it is decoded here, into what the tests assert on.
type (
	savepointDetail struct {
		Stats      []detailStat      `json:"stats"`
		NotCovered *detailNotCovered `json:"not_covered"`
	}
	detailStat struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	detailNotCovered struct {
		FailedHooks []detailFailedHook `json:"failed_hooks"`
	}
	detailFailedHook struct {
		Service   string `json:"service"`
		Replica   int    `json:"replica"`
		Container string `json:"container"`
		Hook      string `json:"hook"`
		ExitCode  *int   `json:"exit_code"`
		Error     string `json:"error"`
	}
)

// detailOf decodes the compose provider's body of savepoint.
func detailOf(t *testing.T, savepoint model.Savepoint) savepointDetail {
	t.Helper()
	require.Equal(t, "compose", savepoint.Provider)
	var detail savepointDetail
	require.NoError(t, json.Unmarshal(savepoint.Detail, &detail))
	return detail
}
