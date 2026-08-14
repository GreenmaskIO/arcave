// Package sandbox will be the sandbox family on the docker runtime — a
// docker-in-docker environment arcave savepoints the same way it savepoints a
// stack. Empty on purpose: the slot is here so the machinery under
// provider/docker/internal is read as shared between two families rather than as
// the compose provider's own.
package sandbox
