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

import "fmt"

// ContainerExitError reports a one-shot Docker container that exited non-zero.
// Like the sentinels in errors.go it crosses the client → service → cli/mcp
// seam, so it lives in model: a consumer that wants the container's full output
// recovers it with errors.As, while Error() — the text that reaches stdout via
// the JSON printer and, later, an MCP tool result an agent holds in context —
// stays compact.
//
// Output is already bounded and demultiplexed by whoever builds the error; the
// producer must never hand it the raw, unbounded, stdcopy-framed log stream.
type ContainerExitError struct {
	// Image is the container image that ran, e.g. "busybox".
	Image string
	// StatusCode is the container's non-zero exit status.
	StatusCode int64
	// Output holds the container's last log lines, already bounded and
	// demultiplexed; it may be empty when the container printed nothing.
	Output []string
}

// Error renders a compact one-line message. It never prints the whole of Output:
// a caller that needs it recovers the value with errors.As.
func (m *ContainerExitError) Error() string {
	if len(m.Output) == 0 {
		return fmt.Sprintf("%s container exited with status %d", m.Image, m.StatusCode)
	}
	// A short tail — the container's last line — is enough to diagnose; the rest
	// stays in Output for errors.As.
	return fmt.Sprintf("%s container exited with status %d: %s",
		m.Image, m.StatusCode, m.Output[len(m.Output)-1])
}
