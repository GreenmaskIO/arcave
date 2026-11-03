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

package model

import (
	"context"
	"io"
	"io/fs"
)

// OneShotSpec describes a throwaway container run to completion, optionally
// exchanging stdio with the caller. It lives in model because it crosses a seam:
// a backend declares the runner interface, docker.Engine implements it, and
// neither imports the other.
type OneShotSpec struct {
	Image string
	// Entrypoint, when set, replaces the image's entrypoint; Cmd becomes its arguments.
	Entrypoint []string
	Cmd        []string
	// Env entries are "KEY=value", the daemon's own shape.
	Env []string
	// Files are written into the container before it starts, so the entrypoint may
	// be one of them; in order, a directory before what it holds.
	Files []OneShotFile
	// Binds are "source:target" mounts: a volume name or a host path.
	Binds []string

	// HostNetwork runs the container in the daemon host's network namespace
	// instead of a bridge — how a container reaches a server bound on the host
	// loopback when no name for the host resolves (native Linux daemons).
	HostNetwork bool

	// Stdin, when set, is streamed to the container's stdin, which is then
	// half-closed so the process sees EOF. Stdout and Stderr receive the
	// demultiplexed output. All three nil means no attach.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Exec, when set, is a second process started in the running container
	// whose stdio is handed to Exec.Serve as one stream while the container runs.
	Exec *OneShotExec
}

// OneShotFile is a directory or a file put into a one-shot container before it starts.
type OneShotFile struct {
	// Path is absolute and clean.
	Path string
	// Mode is the entry's permissions, with fs.ModeDir for a directory.
	Mode fs.FileMode
	// Content is a file's bytes; a directory has none.
	Content []byte
}

// OneShotExec is a process run beside a one-shot container's own, its stdout
// read and its stdin written as one stream.
type OneShotExec struct {
	Cmd []string
	// Stderr receives the process's stderr; nil discards it.
	Stderr io.Writer
	// Serve is handed the stream, which is closed once the container exits or
	// the context ends; it must return then. Its error changes no outcome.
	Serve func(ctx context.Context, stream io.ReadWriteCloser) error
}
