package rest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/registry"
)

// testID is a name of the shape restic writes: 64 lowercase hex characters, and
// stubBody is what the repository below holds under it.
var (
	testID   = model.ObjectID(strings.Repeat("0a1b2c3d", 8))
	stubBody = []byte("0123456789abcdef")
)

// The names the route table addresses. An unserved name is a 404 before any
// operation runs, so every name a case expects an answer from has to be served.
const (
	testRepoName    = "testrepo"
	otherRepoName   = "other"
	flattenedName   = "safety-volume-pgdata"
	unknownRepoName = "nosuchrepo"
)

// errStore is a failure that is none of the protocol's own conditions.
var errStore = errors.New("store unreachable")

// journalled carries a logger the way a command's context does — the only way
// this package's own lines can be read back.
func journalled(w io.Writer) context.Context {
	logger := zerolog.New(w).Level(zerolog.DebugLevel)
	return logger.WithContext(context.Background())
}

// serving is the registry these tests reach a repository through.
func serving(t *testing.T, repos map[string]model.Repository) *registry.Registry {
	t.Helper()

	reg := registry.New()
	for name, repo := range repos {
		require.NoError(t, reg.Register(name, repo))
	}
	return reg
}

// oneRepository serves repo under every name the route table addresses, so a
// case about routing says nothing about resolution.
func oneRepository(t *testing.T, repo model.Repository) *registry.Registry {
	t.Helper()

	return serving(t, map[string]model.Repository{
		testRepoName:  repo,
		otherRepoName: repo,
		flattenedName: repo,
	})
}

var _ model.Repository = stubRepository{}

// stubRepository answers every operation successfully, for the tests about
// routing rather than about the protocol's own answers. Its body is what a read
// returns; the zero value serves stubBody.
type stubRepository struct{ body []byte }

func (s stubRepository) content() []byte {
	if s.body == nil {
		return stubBody
	}
	return s.body
}

func (stubRepository) Create(context.Context) error { return nil }

func (s stubRepository) StatConfig(context.Context) (int64, error) {
	return int64(len(s.content())), nil
}

func (s stubRepository) OpenConfig(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.content())), nil
}

func (stubRepository) SaveConfig(context.Context, io.Reader) error { return nil }

func (stubRepository) DeleteConfig(context.Context) error { return nil }

func (s stubRepository) ListBlobs(context.Context, model.ListBlobsRequest) ([]model.Blob, error) {
	return []model.Blob{{Name: string(testID), Size: int64(len(s.content()))}}, nil
}

func (s stubRepository) StatBlob(context.Context, model.StatBlobRequest) (int64, error) {
	return int64(len(s.content())), nil
}

func (s stubRepository) OpenBlobRange(_ context.Context, req model.OpenBlobRangeRequest) (io.ReadCloser, error) {
	body := s.content()
	if req.Offset+req.Length > int64(len(body)) {
		return nil, fmt.Errorf("the range %d+%d is outside the stub", req.Offset, req.Length)
	}
	return io.NopCloser(bytes.NewReader(body[req.Offset : req.Offset+req.Length])), nil
}

func (stubRepository) SaveBlob(context.Context, model.SaveBlobRequest) error {
	return nil
}

func (stubRepository) DeleteBlob(context.Context, model.DeleteBlobRequest) error {
	return nil
}

// failingRepository answers one operation with a condition the protocol has no
// status for; everything else it inherits.
type failingRepository struct{ stubRepository }

func (failingRepository) OpenConfig(context.Context) (io.ReadCloser, error) { return nil, errStore }

// panickingRepository is the other way an operation can fail to answer.
type panickingRepository struct{ stubRepository }

func (panickingRepository) OpenConfig(context.Context) (io.ReadCloser, error) { panic("boom") }
