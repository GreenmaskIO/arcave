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

import "io"

// OneShotSpec describes a throwaway container run to completion, optionally
// exchanging stdio with the caller. It lives in model because it crosses a seam:
// a backend declares the runner interface, docker.Engine implements it, and
// neither imports the other.
type OneShotSpec struct {
	Image string
	Cmd   []string
	// Env entries are "KEY=value", the daemon's own shape.
	Env []string
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
}
