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

package reporter

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSON_Report(t *testing.T) {
	var out bytes.Buffer
	reporter := NewJSON(&out)

	for _, evt := range newStream() {
		reporter.Report(evt)
	}

	require.NoError(t, reporter.Err())
	assertGolden(t, "stream.ndjson", out.Bytes())
}

func TestJSON_Report_StopsAtAFailedWrite(t *testing.T) {
	errDisk := errors.New("disk gone")
	w := &failingWriter{err: errDisk}
	reporter := NewJSON(w)

	for _, evt := range newStream()[:2] {
		reporter.Report(evt)
	}

	assert.ErrorIs(t, reporter.Err(), errDisk)
	assert.Equal(t, 1, w.writes)
}

type failingWriter struct {
	err    error
	writes int
}

func (w *failingWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, w.err
}
