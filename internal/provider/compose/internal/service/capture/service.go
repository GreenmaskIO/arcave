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

// Package capture stores a savepoint of a compose project already held still: its volumes, the
// images its services run on, their writable layers and the spec, short of the record a commit
// writes last.
package capture

import (
	"context"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/fanout"

	"github.com/greenmaskio/arcave/internal/model"
)

// SavepointRepository writes a capture's spec and commits its record.
type SavepointRepository interface {
	SaveSpec(ctx context.Context, id model.SavepointID, spec []byte) error
	SaveSavepoint(ctx context.Context, savepoint model.Savepoint) error
}

// ImageBackend commits writable layers into the store and keeps the images they were taken over.
type ImageBackend interface {
	CaptureImage(ctx context.Context, req composemodel.ImageCaptureRequest) (model.Image, error)
	CaptureWritableLayer(ctx context.Context, req composemodel.WritableLayerCaptureRequest) (model.WritableLayer, error)
}

// Deps are Capturer's collaborators.
type Deps struct {
	SavepointRepo SavepointRepository
	ImageBackend  ImageBackend
	// Concurrency caps how many units one fan-out works at once; 0 leaves it unbounded.
	Concurrency int
}

// Capturer stores the captures of one compose project.
type Capturer struct {
	savepointRepo           SavepointRepository
	imageBackend            ImageBackend
	maxConcurrentOperations int
}

// New builds a Capturer from deps.
func New(deps Deps) *Capturer {
	return &Capturer{
		savepointRepo:           deps.SavepointRepo,
		imageBackend:            deps.ImageBackend,
		maxConcurrentOperations: fanout.Limit(deps.Concurrency),
	}
}
