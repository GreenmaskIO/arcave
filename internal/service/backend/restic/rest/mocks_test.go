package rest

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/stretchr/testify/mock"
)

// The mock forwards all of its seam's arguments to Called, ctx included; tests
// match ctx with mock.Anything unless they assert on it.

// repositoryMock is the handler's view of a repository, doubled. It is what
// makes the seam worth having: the handler is exercised without a store, a
// filesystem or a single real object.
type repositoryMock struct {
	mock.Mock
}

// compile-time assertion: the double must keep up with the interface.
var _ repository = (*repositoryMock)(nil)

// newMockedHandler returns the handler over a fresh double, plus the double.
func newMockedHandler() (http.Handler, *repositoryMock) {
	repo := &repositoryMock{}
	return newHandler(repo), repo
}

func (m *repositoryMock) Create(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func (m *repositoryMock) StatConfig(ctx context.Context) (int64, error) {
	args := m.Called(ctx)
	return args.Get(0).(int64), args.Error(1)
}

func (m *repositoryMock) OpenConfig(ctx context.Context) (io.ReadCloser, error) {
	args := m.Called(ctx)
	rc, _ := args.Get(0).(io.ReadCloser)
	return rc, args.Error(1)
}

func (m *repositoryMock) SaveConfig(ctx context.Context, body io.Reader) error {
	return m.Called(ctx, body).Error(0)
}

func (m *repositoryMock) DeleteConfig(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func (m *repositoryMock) ListBlobs(ctx context.Context, objectType string) ([]blob, error) {
	args := m.Called(ctx, objectType)
	blobs, _ := args.Get(0).([]blob)
	return blobs, args.Error(1)
}

func (m *repositoryMock) StatBlob(ctx context.Context, objectType, name string) (int64, error) {
	args := m.Called(ctx, objectType, name)
	return args.Get(0).(int64), args.Error(1)
}

func (m *repositoryMock) OpenBlob(ctx context.Context, objectType, name string) (io.ReadSeekCloser, error) {
	args := m.Called(ctx, objectType, name)
	f, _ := args.Get(0).(io.ReadSeekCloser)
	return f, args.Error(1)
}

func (m *repositoryMock) SaveBlob(ctx context.Context, objectType, name string, body io.Reader) error {
	return m.Called(ctx, objectType, name, body).Error(0)
}

func (m *repositoryMock) DeleteBlob(ctx context.Context, objectType, name string) error {
	return m.Called(ctx, objectType, name).Error(0)
}

// readSeekCloser is an in-memory stand-in for what OpenBlob returns, which
// http.ServeContent seeks over to measure the object.
type readSeekCloser struct {
	*strings.Reader
}

func newReadSeekCloser(payload string) io.ReadSeekCloser {
	return readSeekCloser{Reader: strings.NewReader(payload)}
}

func (readSeekCloser) Close() error { return nil }
