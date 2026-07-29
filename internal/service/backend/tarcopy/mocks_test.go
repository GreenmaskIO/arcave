package tarcopy

import (
	"context"
	"io"

	"github.com/stretchr/testify/mock"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

type mockMountStreamer struct{ mock.Mock }

func (m *mockMountStreamer) OpenMount(ctx context.Context, project, service, target string) (io.ReadCloser, error) {
	args := m.Called(ctx, project, service, target)
	rc, _ := args.Get(0).(io.ReadCloser)
	return rc, args.Error(1)
}

func (m *mockMountStreamer) WriteMount(ctx context.Context, project, service, target string, r io.Reader) error {
	return m.Called(ctx, project, service, target, r).Error(0)
}

// failingReader fails mid-stream; a mock cannot, since it only hands the reader over.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }
