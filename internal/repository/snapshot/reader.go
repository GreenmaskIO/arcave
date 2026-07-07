package snapshot

import (
	"context"
	"io"

	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/volume"
)

// reader streams a stored object back out.
type reader struct {
	st model.Storager
	// objectPath is the source path relative to the Storager's root — a nested
	// key like "1780335122/db/export/data/data.tar", not just a filename.
	objectPath string
}

var _ volume.Reader = (*reader)(nil)

// NewReader returns a volume.Reader that streams objectPath out of st.
func NewReader(st model.Storager, objectPath string) volume.Reader {
	return &reader{st: st, objectPath: objectPath}
}

func (r *reader) Open(ctx context.Context) (io.ReadCloser, error) {
	return r.st.GetObject(ctx, r.objectPath)
}

func (r *reader) DebugInfo() map[string]any {
	return map[string]any{"object_path": r.objectPath}
}
