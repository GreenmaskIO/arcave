package imagecopy

import (
	"context"
	"io"

	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
)

type mockContainerImager struct{ mock.Mock }

func (m *mockContainerImager) CommitContainer(ctx context.Context, containerID string) (model.ImageDigestID, error) {
	args := m.Called(ctx, containerID)
	return args.Get(0).(model.ImageDigestID), args.Error(1)
}

func (m *mockContainerImager) SaveImage(ctx context.Context, imageID model.ImageDigestID) (io.ReadCloser, error) {
	args := m.Called(ctx, imageID)
	rc, _ := args.Get(0).(io.ReadCloser)
	return rc, args.Error(1)
}

func (m *mockContainerImager) RemoveImage(ctx context.Context, imageID model.ImageDigestID) error {
	return m.Called(ctx, imageID).Error(0)
}

// LoadImage mirrors the daemon: it reports a failed load in-band and answers 200,
// so a stream that broke mid-read is not what the caller hears about.
func (m *mockContainerImager) LoadImage(ctx context.Context, r io.Reader) error {
	archive, _ := io.ReadAll(r)
	return m.Called(ctx, string(archive)).Error(0)
}

func (m *mockContainerImager) TagImage(ctx context.Context, imageID model.ImageDigestID, ref string) error {
	return m.Called(ctx, imageID, ref).Error(0)
}

func (m *mockContainerImager) ImageExists(ctx context.Context, imageID model.ImageDigestID) (bool, error) {
	args := m.Called(ctx, imageID)
	return args.Bool(0), args.Error(1)
}
