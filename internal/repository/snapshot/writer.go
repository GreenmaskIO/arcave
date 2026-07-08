package snapshot

import (
	"context"
	"io"

	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/volume"
)

// writer persists a tar stream to a Storager object.
type writer struct {
	st model.Storager
	// objectPath is the destination path relative to the Storager's root — a
	// nested key like "1780335122/db/export/data/data.tar", not just a filename.
	objectPath string
}

var _ volume.Writer = (*writer)(nil)

// NewWriter returns a volume.Writer that stores the stream at objectPath in st.
func NewWriter(st model.Storager, objectPath string) volume.Writer {
	return &writer{st: st, objectPath: objectPath}
}

func (w *writer) Write(ctx context.Context, r io.Reader) error {
	return w.st.PutObject(ctx, w.objectPath, io.NopCloser(r))
}

func (w *writer) DebugInfo() map[string]any {
	return map[string]any{"object_path": w.objectPath}
}
