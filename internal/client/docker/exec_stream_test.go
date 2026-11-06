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

package docker

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The stream reads the exec's demultiplexed stdout, through to its end, and
// hands its stderr to the writer it was opened with.
func TestExecStream_Read(t *testing.T) {
	// Arrange
	hj := newFakeHijack()
	go func() {
		_, _ = io.WriteString(hj.stdout, "out-1 ")
		_, _ = io.WriteString(hj.stderr, "err")
		_, _ = io.WriteString(hj.stdout, "out-2")
		hj.closeOutput()
	}()
	var stderr bytes.Buffer
	stream := newExecStream(hj.resp.HijackedResponse, &stderr)

	// Act
	got, err := io.ReadAll(stream)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "out-1 out-2", string(got))
	assert.Equal(t, "err", stderr.String())
}

// Writes reach the exec's stdin verbatim.
func TestExecStream_Write(t *testing.T) {
	// Arrange
	hj := newFakeHijack()
	stdinGot := hj.collectStdin()
	stream := newExecStream(hj.resp.HijackedResponse, nil)

	// Act
	_, err := io.WriteString(stream, "request")
	closeErr := stream.Close()

	// Assert
	require.NoError(t, err)
	require.NoError(t, closeErr)
	assert.Equal(t, "request", string(<-stdinGot))
}

// Close drops the exec's connection and unblocks a pending read; a second Close
// is a no-op.
func TestExecStream_Close(t *testing.T) {
	// Arrange
	hj := newFakeHijack()
	stream := newExecStream(hj.resp.HijackedResponse, nil)
	readDone := make(chan error, 1)
	go func() {
		_, err := stream.Read(make([]byte, 1))
		readDone <- err
	}()

	// Act
	firstErr := stream.Close()
	secondErr := stream.Close()

	// Assert
	require.NoError(t, firstErr)
	require.NoError(t, secondErr)
	assertConnClosed(t, hj, "Close must drop the exec's connection")
	assert.ErrorIs(t, <-readDone, io.ErrClosedPipe, "a pending read must end once the stream is closed")
}

// A connection that breaks mid-stream reaches the reader as its error, not as EOF.
func TestExecStream_Read_BrokenStream(t *testing.T) {
	// Arrange
	broken := errors.New("connection reset")
	hj := newFakeHijack()
	go func() {
		_, _ = io.WriteString(hj.stdout, "partial")
		_ = hj.outW.CloseWithError(broken)
	}()
	stream := newExecStream(hj.resp.HijackedResponse, nil)

	// Act
	got, err := io.ReadAll(stream)

	// Assert
	assert.ErrorIs(t, err, broken)
	assert.Equal(t, "partial", string(got))
}
