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
func (e *ContainerExitError) Error() string {
	if len(e.Output) == 0 {
		return fmt.Sprintf("%s container exited with status %d", e.Image, e.StatusCode)
	}
	// A short tail — the container's last line — is enough to diagnose; the rest
	// stays in Output for errors.As.
	return fmt.Sprintf("%s container exited with status %d: %s",
		e.Image, e.StatusCode, e.Output[len(e.Output)-1])
}
