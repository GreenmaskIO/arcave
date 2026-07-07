// Package volume is the engine-agnostic core of GM's volume snapshotting. It
// realises the spec's Reader → Writer → combiner shape: a Reader streams one
// volume's data out as a tar archive, a Writer persists that stream, and Copy
// wires the two together while tallying bytes.
//
// It imports only the standard library and the byte-counting wrappers in
// internal/utils/ioutils — never the Docker SDK or a storage backend. The
// concrete endpoints (Docker containers, the snapshot/backup stores) live in
// their own packages and are injected as Reader/Writer, so export and restore
// are the same Copy call with the endpoints swapped.
package volume

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/greenmaskio/gm-tools/internal/utils/ioutils"
)

// Reader streams one volume's contents as a tar archive (the pull side).
type Reader interface {
	// Open begins producing the archive, returning the stream to pull from. The
	// caller owns the returned ReadCloser and closes it.
	Open(ctx context.Context) (io.ReadCloser, error)
	// DebugInfo carries identifying metadata (service, mount, object key) for logs.
	DebugInfo() map[string]any
}

// Writer consumes a tar stream and persists it, returning the number of bytes
// stored (the push side).
type Writer interface {
	Write(ctx context.Context, r io.Reader) (int64, error)
	DebugInfo() map[string]any
}

// Stat is the per-copy result, assembled from the counting wrapper around the
// source and the writer's stored-byte count plus wall-clock timing.
type Stat struct {
	BytesRead   int64 // uncompressed tar bytes pulled from the source
	BytesStored int64 // bytes the writer persisted
	Duration    time.Duration
}

// Copy runs one volume copy: it opens the source, wraps it in a counting reader,
// and hands that to the writer, which pulls at storage speed — intrinsic
// backpressure, no io.Pipe needed. The source is the only closer here (the
// writer finalises inside Write and has no Close), so Copy always closes it,
// even on a write error, and reports the first error it hits.
func Copy(ctx context.Context, r Reader, w Writer) (Stat, error) {
	start := time.Now()

	src, err := r.Open(ctx)
	if err != nil {
		return Stat{}, fmt.Errorf("open source: %w", err)
	}
	counted := ioutils.NewReader(src)

	stored, writeErr := w.Write(ctx, counted)
	closeErr := src.Close()

	switch {
	case writeErr != nil:
		return Stat{}, fmt.Errorf("write archive: %w", writeErr)
	case closeErr != nil:
		return Stat{}, fmt.Errorf("close source: %w", closeErr)
	}

	return Stat{
		BytesRead:   counted.GetCount(),
		BytesStored: stored,
		Duration:    time.Since(start),
	}, nil
}
