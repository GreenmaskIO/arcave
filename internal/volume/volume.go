// Package volume is the engine-agnostic core of GM's volume snapshotting. It
// realises the spec's Reader → Writer → combiner shape: a Reader streams one
// volume's data out as a tar archive, a Writer persists that stream, and Copy
// wires the two together.
//
// It imports only the standard library — never the Docker SDK or a storage
// backend. The concrete endpoints (Docker containers, the snapshot/backup
// stores) live in their own packages and are injected as Reader/Writer, so
// export and restore are the same Copy call with the endpoints swapped.
package volume

import (
	"context"
	"fmt"
	"io"
)

// Reader streams one volume's contents as a tar archive (the pull side).
type Reader interface {
	// Open begins producing the archive, returning the stream to pull from. The
	// caller owns the returned ReadCloser and closes it.
	Open(ctx context.Context) (io.ReadCloser, error)
	// DebugInfo carries identifying metadata (service, mount, object key) for logs.
	DebugInfo() map[string]any
}

// Writer consumes a tar stream and persists it (the push side).
type Writer interface {
	Write(ctx context.Context, r io.Reader) error
	DebugInfo() map[string]any
}

// Copy runs one volume copy: it opens the source and hands the stream to the
// writer, which pulls at storage speed — intrinsic backpressure, no io.Pipe
// needed. The source is the only closer here (the writer finalises inside Write
// and has no Close), so Copy always closes it, even on a write error, and
// reports the first error it hits.
func Copy(ctx context.Context, r Reader, w Writer) error {
	src, err := r.Open(ctx)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}

	writeErr := w.Write(ctx, src)
	closeErr := src.Close()

	switch {
	case writeErr != nil:
		return fmt.Errorf("write archive: %w", writeErr)
	case closeErr != nil:
		return fmt.Errorf("close source: %w", closeErr)
	}

	return nil
}
