package fullcopy

import (
	"context"
	"io"

	"github.com/stretchr/testify/mock"

	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

type mockVolumeStreamer struct{ mock.Mock }

func (m *mockVolumeStreamer) ResolveMountSource(
	ctx context.Context, v dockermodel.Volume,
) (dockermodel.MountSource, error) {
	args := m.Called(ctx, v)
	src, _ := args.Get(0).(dockermodel.MountSource)
	return src, args.Error(1)
}

func (m *mockVolumeStreamer) OpenVolume(
	ctx context.Context, src dockermodel.MountSource,
) (io.ReadCloser, error) {
	args := m.Called(ctx, src)
	rc, _ := args.Get(0).(io.ReadCloser)
	return rc, args.Error(1)
}

func (m *mockVolumeStreamer) WriteVolume(ctx context.Context, src dockermodel.MountSource, r io.Reader) error {
	return m.Called(ctx, src, r).Error(0)
}

// failingReader fails mid-stream; a mock cannot, since it only hands the reader over.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }
