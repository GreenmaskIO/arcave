package model

import (
	"context"
	"io"
)

// Repository is the seam between the protocol and whatever keeps the objects.
// It lives here so the assembly and the doubles can name it too.
type Repository interface {
	Create(ctx context.Context) error

	StatConfig(ctx context.Context) (int64, error)
	OpenConfig(ctx context.Context) (io.ReadCloser, error)
	SaveConfig(ctx context.Context, body io.Reader) error
	DeleteConfig(ctx context.Context) error

	ListBlobs(ctx context.Context, req ListBlobsRequest) ([]Blob, error)
	StatBlob(ctx context.Context, req StatBlobRequest) (int64, error)
	OpenBlobRange(ctx context.Context, req OpenBlobRangeRequest) (io.ReadCloser, error)
	SaveBlob(ctx context.Context, req SaveBlobRequest) error
	DeleteBlob(ctx context.Context, req DeleteBlobRequest) error
}
