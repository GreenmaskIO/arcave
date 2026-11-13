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

package capture

import (
	"cmp"
	"context"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// revertBudget bounds the step that brings the compose project back past an interrupt.
const revertBudget = 10 * time.Minute

// savepointRepository writes a capture's spec and commits its record.
type savepointRepository interface {
	SaveSpec(ctx context.Context, id model.SavepointID, spec []byte) error
	SaveSavepoint(ctx context.Context, savepoint model.Savepoint) error
}

// imageBackend commits writable layers into the store and keeps the images they were taken over.
type imageBackend interface {
	CaptureImage(ctx context.Context, req model.ImageCaptureRequest) (model.Image, error)
	CaptureWritableLayer(ctx context.Context, req model.WritableLayerCaptureRequest) (model.WritableLayer, error)
}

// composeRunner stops the compose project for a capture and starts it after.
type composeRunner interface {
	// Stop stops every service of project and returns the ids of the containers its deadline
	// killed.
	Stop(ctx context.Context, project *types.Project) ([]string, error)
	// StopServices stops the named services alone, and returns the same.
	StopServices(ctx context.Context, project *types.Project, services []string) ([]string, error)
	// Start starts every service of project, whichever of them was stopped, and waits on their
	// health checks; a running container is left as it is.
	Start(ctx context.Context, project *types.Project, waitTimeout time.Duration) error
}

// containerControl freezes containers for a capture and resumes them after.
type containerControl interface {
	PauseContainers(ctx context.Context, ids []string) error
	// UnpauseContainers resumes whichever of the containers is paused; one that is not, or is
	// gone, is no error.
	UnpauseContainers(ctx context.Context, ids []string) error
	// AwaitHealthProbes waits out the unhealthy a pause leaves on the containers until their next
	// probe, bounded by their healthchecks.
	AwaitHealthProbes(ctx context.Context, ids []string) error
}

// Deps are Service's collaborators.
type Deps struct {
	SavepointRepo savepointRepository
	ImageBackend  imageBackend
	ComposeRunner composeRunner
	// ContainerControl pauses the services captured with model.CaptureModePause.
	ContainerControl containerControl
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

// Service captures a savepoint of a compose project in three steps the caller composes: Hold
// holds the project still, Capture captures and commits what is held, Release brings it back.
type Service struct {
	savepointRepo           savepointRepository
	imageBackend            imageBackend
	composeRunner           composeRunner
	containerControl        containerControl
	store                   storages.Storager
	maxConcurrentOperations int
	runnerInfo              heartbeat.RunnerInfo
	heartbeatInterval       time.Duration
	heartbeatStaleAfter     time.Duration
}

// New builds a Service from deps.
func New(deps Deps) *Service {
	return &Service{
		savepointRepo:           deps.SavepointRepo,
		imageBackend:            deps.ImageBackend,
		composeRunner:           deps.ComposeRunner,
		containerControl:        deps.ContainerControl,
		store:                   deps.Store,
		maxConcurrentOperations: fanOutLimit(deps.Concurrency),
		runnerInfo:              deps.RunnerInfo,
		heartbeatInterval:       cmp.Or(deps.HeartbeatInterval, heartbeat.DefaultInterval),
		heartbeatStaleAfter:     cmp.Or(deps.HeartbeatStaleAfter, heartbeat.DefaultStaleAfter),
	}
}

// Hold holds the project still as plan says and returns the containers the stop killed; the
// caller captures with Capture and brings the project back with Release, or takes it down.
func (s *Service) Hold(ctx context.Context, plan Plan) (_ []string, err error) {
	ctx, step := progress.Run(ctx, progress.Step{Kind: kindSavepointHold}, progress.MeasureResources)
	defer func() { step.End(err) }()

	return newCaptureTriggersByMode(s.composeRunner, s.containerControl, plan).beforeCapture(ctx)
}

// Release brings back the project plan holds, on a revertContext; it converges whether Hold
// ran, failed partway or never ran.
func (s *Service) Release(ctx context.Context, plan Plan) (err error) {
	revertCtx, cancel := revertContext(ctx)
	defer cancel()
	revertCtx, step := progress.Run(revertCtx, progress.Step{Kind: kindSavepointRelease}, progress.MeasureResources)
	defer func() { step.End(err) }()

	return newCaptureTriggersByMode(s.composeRunner, s.containerControl, plan).afterCapture(revertCtx)
}

// revertContext strips ctx of its cancellation and bounds it by revertBudget instead, for a
// step that puts the environment back and so must outlive a Ctrl-C.
func revertContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), revertBudget)
}

// fanOutLimit turns a configured concurrency into an errgroup limit, where a
// negative one is unbounded and zero would admit no goroutine at all.
func fanOutLimit(concurrency int) int {
	if concurrency < 1 {
		return -1
	}
	return concurrency
}
