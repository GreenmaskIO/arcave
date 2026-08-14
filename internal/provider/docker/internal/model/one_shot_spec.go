package model

import "io"

// OneShotSpec describes a throwaway container run to completion, optionally
// exchanging stdio with the caller. It lives in model because it crosses a seam:
// a backend declares the runner interface, docker.Engine implements it, and
// neither imports the other.
type OneShotSpec struct {
	Image string
	Cmd   []string
	// Env entries are "KEY=value", the arcaved's own shape.
	Env []string
	// Binds are "source:target" mounts: a volume name or a host path.
	Binds []string

	// Stdin, when set, is streamed to the container's stdin, which is then
	// half-closed so the process sees EOF. Stdout and Stderr receive the
	// demultiplexed output. All three nil means no attach.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}
