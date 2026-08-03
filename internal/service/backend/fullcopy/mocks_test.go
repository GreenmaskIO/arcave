package fullcopy

import (
	"context"
	"io"

	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

type mockVolumeStreamer struct{ mock.Mock }

func (m *mockVolumeStreamer) ResolveVolumeAddress(
	ctx context.Context, v model.Volume,
) (model.VolumeAddress, error) {
	args := m.Called(ctx, v)
	addr, _ := args.Get(0).(model.VolumeAddress)
	return addr, args.Error(1)
}

func (m *mockVolumeStreamer) OpenVolume(
	ctx context.Context, addr model.VolumeAddress,
) (io.ReadCloser, error) {
	args := m.Called(ctx, addr)
	rc, _ := args.Get(0).(io.ReadCloser)
	return rc, args.Error(1)
}

func (m *mockVolumeStreamer) WriteVolume(ctx context.Context, addr model.VolumeAddress, r io.Reader) error {
	return m.Called(ctx, addr, r).Error(0)
}

// failingReader fails mid-stream; a mock cannot, since it only hands the reader over.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }
