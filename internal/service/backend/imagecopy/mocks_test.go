package imagecopy

import (
	"context"
	"io"

	"github.com/stretchr/testify/mock"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

type mockContainerImager struct{ mock.Mock }

func (m *mockContainerImager) CommitContainer(ctx context.Context, containerID string) (string, error) {
	args := m.Called(ctx, containerID)
	return args.String(0), args.Error(1)
}

func (m *mockContainerImager) SaveImage(ctx context.Context, imageID string) (io.ReadCloser, error) {
	args := m.Called(ctx, imageID)
	rc, _ := args.Get(0).(io.ReadCloser)
	return rc, args.Error(1)
}

func (m *mockContainerImager) RemoveImage(ctx context.Context, imageID string) error {
	return m.Called(ctx, imageID).Error(0)
}

func (m *mockContainerImager) LoadImage(ctx context.Context, r io.Reader) error {
	return m.Called(ctx, r).Error(0)
}

func (m *mockContainerImager) TagImage(ctx context.Context, imageID, ref string) error {
	return m.Called(ctx, imageID, ref).Error(0)
}

func (m *mockContainerImager) ImageExists(ctx context.Context, imageID string) (bool, error) {
	args := m.Called(ctx, imageID)
	return args.Bool(0), args.Error(1)
}

// failingReader fails mid-stream; a mock cannot, since it only hands the reader over.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }
