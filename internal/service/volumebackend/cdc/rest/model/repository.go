// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
