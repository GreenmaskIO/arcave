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
	"cmp"
	"context"
	"time"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
)

// SavepointRepository writes a capture's spec and commits its record.
type SavepointRepository interface {
	SaveSpec(ctx context.Context, id model.SavepointID, spec []byte) error
	SaveSavepoint(ctx context.Context, savepoint model.Savepoint) error
}

// ImageBackend commits writable layers into the store and keeps the images they were taken over.
type ImageBackend interface {
	CaptureImage(ctx context.Context, req model.ImageCaptureRequest) (model.Image, error)
	CaptureWritableLayer(ctx context.Context, req model.WritableLayerCaptureRequest) (model.WritableLayer, error)
}

// Deps are Capturer's collaborators.
type Deps struct {
	SavepointRepo SavepointRepository
	ImageBackend  ImageBackend
	// Store is the compose project's store: the capture's tree and its heartbeat.
	Store storages.Storager
	// Concurrency caps how many units one fan-out works at once; 0 leaves it unbounded.
	Concurrency int
	// RunnerInfo identifies this process in a capture's heartbeat.
	RunnerInfo heartbeat.RunnerInfo
	// HeartbeatInterval and HeartbeatStaleAfter time a capture's heartbeat; zero keeps
	// heartbeat.DefaultInterval and heartbeat.DefaultStaleAfter.
	HeartbeatInterval   time.Duration
	HeartbeatStaleAfter time.Duration
}

// Capturer stores the captures of one compose project.
type Capturer struct {
	savepointRepo           SavepointRepository
	imageBackend            ImageBackend
	store                   storages.Storager
	maxConcurrentOperations int
	runnerInfo              heartbeat.RunnerInfo
	heartbeatInterval       time.Duration
	heartbeatStaleAfter     time.Duration
}

// New builds a Capturer from deps.
func New(deps Deps) *Capturer {
	return &Capturer{
		savepointRepo:           deps.SavepointRepo,
		imageBackend:            deps.ImageBackend,
		store:                   deps.Store,
		maxConcurrentOperations: fanOutLimit(deps.Concurrency),
		runnerInfo:              deps.RunnerInfo,
		heartbeatInterval:       cmp.Or(deps.HeartbeatInterval, heartbeat.DefaultInterval),
		heartbeatStaleAfter:     cmp.Or(deps.HeartbeatStaleAfter, heartbeat.DefaultStaleAfter),
	}
}

// fanOutLimit turns a configured concurrency into an errgroup limit, where a negative one is
// unbounded and zero would admit no goroutine at all.
func fanOutLimit(concurrency int) int {
	if concurrency < 1 {
		return -1
	}
	return concurrency
}
