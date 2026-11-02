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
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/greenmaskio/arcave/internal/model"
)

// execStream is an exec's stdio as one stream: its stdout read, its stdin
// written. Its stderr goes to the writer it was opened with.
type execStream struct {
	hijacked types.HijackedResponse
	pr       *io.PipeReader
	// demuxed is closed once the demultiplexer has stopped writing.
	demuxed chan struct{}
	once    sync.Once
}

var _ io.ReadWriteCloser = (*execStream)(nil)

// newExecStream demultiplexes hijacked in the background: stdout into the
// stream's reads, stderr into stderr.
func newExecStream(hijacked types.HijackedResponse, stderr io.Writer) *execStream {
	if stderr == nil {
		stderr = io.Discard
	}
	pr, pw := io.Pipe()
	demuxed := make(chan struct{})
	go func() {
		defer close(demuxed)
		_, err := stdcopy.StdCopy(pw, stderr, hijacked.Reader)
		//nolint:errcheck // the error travels to the reader
		pw.CloseWithError(err)
	}()
	return &execStream{
		hijacked: hijacked,
		pr:       pr,
		demuxed:  demuxed,
	}
}

func (s *execStream) Read(p []byte) (int, error)  { return s.pr.Read(p) }
func (s *execStream) Write(p []byte) (int, error) { return s.hijacked.Conn.Write(p) }

// Close is release; it never fails.
func (s *execStream) Close() error {
	s.release()
	return nil
}

// release drops the exec's connection, once, and returns when nothing writes to
// the exec's stderr writer any more.
func (s *execStream) release() {
	s.once.Do(func() {
		s.hijacked.Close()
		//nolint:errcheck // a PipeReader's Close does not fail
		s.pr.Close()
	})
	<-s.demuxed
}

// openExecStream starts exec in the running container containerID and returns
// its stdio as one stream.
func openExecStream(
	ctx context.Context, api dockerAPI, containerID string, exec model.OneShotExec,
) (*execStream, error) {
	created, err := api.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          exec.Cmd,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		// A tty would mangle the binary stream and merge stderr into it.
		Tty: false,
	})
	if err != nil {
		return nil, fmt.Errorf("create exec in container %s: %w", containerID, err)
	}
	hijacked, err := api.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("attach to exec %s: %w", created.ID, err)
	}
	return newExecStream(hijacked, exec.Stderr), nil
}
