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

package workload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/container"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/sessionstore"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
)

// A capture before a restore reads the restoring record before anything is planned; a record
// missing or unreadable refuses it, and is no capture policy error: the policy was never resolved.
func TestLive_PlanCapture_RestoringRecord(t *testing.T) {
	record := composetest.DemoRecord()
	errRead := errors.New("store unreachable")
	tests := []struct {
		name    string
		readErr error
		wantErr error
	}{
		{name: "a record not found", wantErr: sessionstore.ErrRecordNotFound},
		{name: "a record that cannot be read", readErr: errRead, wantErr: errRead},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: a refusal plans nothing, so the assembler carries no expectation.
			repo := &composetest.MockSavepointRepo{}
			repo.On("LoadSavepoints", mock.Anything, []model.SavepointID{record.Header.ID}).Return(nil, tt.readErr).Once()
			assembler := &composetest.MockAssembler{}
			svc := newComposeWorkload(Deps{
				SavepointRepo:    repo,
				ProjectAssembler: assembler,
				CaptureDefaults:  composetest.DemoCaptureDefaults,
			})
			live := liveWorkload{service: svc, introspection: composetest.DemoIntrospection()}
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			plan, err := live.PlanCapture(ctx, provider.CaptureOptions{Restoring: record.Header.ID})

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
			_, isPolicyErr := errors.AsType[*provider.CapturePolicyError](err)
			assert.False(t, isPolicyErr)
			assert.Nil(t, plan)
			repo.AssertExpectations(t)
			assembler.AssertExpectations(t)
		})
	}
}

// A savepoint is prepared before anything is stopped: record, backends, spec, then layers loaded
// and the spec pointed at them; whatever refuses it refuses there, in that order.
func TestLive_PrepareSavepoint(t *testing.T) {
	errLoad := errors.New("daemon refused the archive")
	noSpec := composetest.DemoRecordWithImages()
	noSpec.Detail.Spec = nil
	tests := []struct {
		name      string
		savepoint composemodel.Savepoint
		found     bool
		loadErr   error
		wantErr   error
	}{
		{name: "a savepoint with its layer", savepoint: composetest.DemoRecordWithImages(), found: true},
		{name: "a record not found", savepoint: composetest.DemoRecordWithImages(), wantErr: sessionstore.ErrRecordNotFound},
		{name: "a record with no spec", savepoint: noSpec, found: true, wantErr: composemodel.ErrSavepointNoSpec},
		{name: "a layer the daemon will not take", savepoint: composetest.DemoRecordWithImages(), found: true, loadErr: errLoad, wantErr: errLoad},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := &composetest.MockSavepointRepo{}
			var found []composemodel.Savepoint
			if tt.found {
				found = []composemodel.Savepoint{
					tt.savepoint,
				}
			}
			calls := []*mock.Call{repo.On("LoadSavepoints", mock.Anything, []model.SavepointID{tt.savepoint.Header.ID}).Return(found, nil).Once()}
			store := composetest.NewDirStore(t)
			registry := &composetest.MockRegistry{}
			images := &composetest.MockImageBackend{}
			codec := &composetest.MockCodec{}
			if tt.found {
				calls = append(calls, registry.On("New", composetest.MatchSpec(store, "volume-named-demo_db")).Return(composetest.NewMockBackend(composetest.DemoVolume()), nil).Once())
			}
			if tt.found && tt.savepoint.Detail.Spec != nil {
				calls = append(calls,
					repo.On("LoadSpec", mock.Anything, tt.savepoint.Header.ID).Return(composetest.DemoTargetSpecBytes, nil).Once(),
					codec.On("Parse", mock.Anything, composetest.DemoTargetSpecBytes).Return(composetest.DemoTargetSpec, nil).Once(),
					images.On("RestoreWritableLayer", mock.Anything, composemodel.WritableLayerRestoreRequest{
						SnapshotID: tt.savepoint.Header.ID, Layer: composetest.DemoWritableLayer(), Tag: composetest.DemoRestoredTag(1),
					}).Return(composetest.DemoWritableLayerRestoreResult(), tt.loadErr).Once())
			}
			mock.InOrder(calls...)
			svc := newComposeWorkload(Deps{
				SavepointRepo:   repo,
				SpecCodec:       codec,
				BackendRegistry: registry,
				ImageBackend:    images,
				ImageLookup:     &composetest.MockImageLookup{},
				Store:           store,
			})
			live := liveWorkload{service: svc, introspection: composetest.DemoIntrospectionWithContainers()}
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			prepared, err := live.PrepareSavepoint(ctx, tt.savepoint.Header.ID, provider.PrepareOptions{})

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, prepared)
			} else {
				require.NoError(t, err)
				target := prepared.(preparedSavepoint)
				assert.Equal(t, composetest.DemoRestoredTag(1), target.Project.Services[composetest.DemoService].Image)
				assert.Equal(t, types.PullPolicyNever, target.Project.Services[composetest.DemoService].PullPolicy)
				assert.Equal(t, composemodel.RestoreDetail{
					Volumes:        []composemodel.Volume{composetest.DemoVolume()},
					WritableLayers: []composemodel.WritableLayerRestoreResult{composetest.DemoWritableLayerRestoreResult()},
				}, restoreDetailOf(t, prepared.Summary()))
			}
			repo.AssertExpectations(t)
			registry.AssertExpectations(t)
			images.AssertExpectations(t)
			codec.AssertExpectations(t)
		})
	}
}

// Over an absent compose project a leftover volume the savepoint restores into is refused under the
// refusing policy, else named as overwritten; a recovery is never refused.
func TestLive_PrepareSavepoint_LeftoverVolumes(t *testing.T) {
	tests := []struct {
		name            string
		policy          composeconfig.LeftoverVolumesPolicy
		recovering      bool
		wantOverwritten []string
		wantWarnings    []model.Notice
		wantLogged      string
		wantErr         error
	}{
		{
			name:            "the default overwrites",
			wantOverwritten: []string{"demo_db"},
			wantWarnings:    composemodel.NewLeftoverVolumeNotices([]string{"demo_db"}),
		},
		{
			name:            "overwrite",
			policy:          composeconfig.LeftoverVolumesOverwrite,
			wantOverwritten: []string{"demo_db"},
			wantWarnings:    composemodel.NewLeftoverVolumeNotices([]string{"demo_db"}),
		},
		{name: "refuse", policy: composeconfig.LeftoverVolumesRefuse, wantErr: composemodel.ErrLeftoverVolumes},
		{
			name:       "refuse, but a recovery, which names none: they are its own writes",
			policy:     composeconfig.LeftoverVolumesRefuse,
			recovering: true,
			wantLogged: "the compose project is not running but this volume survives",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := composetest.DemoRecord()
			repo := &composetest.MockSavepointRepo{}
			read := expectRecordRead(repo, record)
			store := composetest.NewDirStore(t)
			registry := &composetest.MockRegistry{}
			codec := &composetest.MockCodec{}
			if tt.wantErr == nil {
				mock.InOrder(
					read,
					registry.On("New", composetest.MatchSpec(store, "volume-named-demo_db")).Return(composetest.NewMockBackend(composetest.DemoVolume()), nil).Once(),
					repo.On("LoadSpec", mock.Anything, record.Header.ID).Return(composetest.DemoTargetSpecBytes, nil).Once(),
					codec.On("Parse", mock.Anything, composetest.DemoTargetSpecBytes).Return(composetest.DemoTargetSpec, nil).Once(),
				)
			}
			svc := newComposeWorkload(Deps{
				SavepointRepo:   repo,
				SpecCodec:       codec,
				BackendRegistry: registry,
				ImageLookup:     &composetest.MockImageLookup{},
				LeftoverVolumes: tt.policy,
				Store:           store,
			})
			live := liveWorkload{service: svc, introspection: composetest.DemoAbsentIntrospection()}
			var logs bytes.Buffer
			ctx := zerolog.New(&logs).WithContext(context.Background())

			// Act
			prepared, err := live.PrepareSavepoint(ctx, record.Header.ID, provider.PrepareOptions{Recovering: tt.recovering})

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.EqualError(t, err, composemodel.ErrLeftoverVolumes.Error()+": demo_db")
			} else {
				require.NoError(t, err)
				summary := prepared.Summary()
				assert.Equal(t, tt.wantOverwritten, summary.Overwritten)
				assert.Equal(t, tt.wantWarnings, summary.Warnings)
				assert.Equal(t, tt.wantWarnings, summary.Restored.Notices)
				assert.Contains(t, logs.String(), tt.wantLogged)
			}
			repo.AssertExpectations(t)
			registry.AssertExpectations(t)
			codec.AssertExpectations(t)
		})
	}
}

// A live teardown resumes the replicas found frozen, then runs `compose down`; a failed resume is
// only logged, and over an absent compose project it removes nothing.
func TestLive_Teardown(t *testing.T) {
	paused := composetest.DemoIntrospection()
	paused.Project.Services[0].Containers = []composemodel.Container{{
		ID:     composetest.DemoContainerID,
		Name:   composetest.DemoContainerName(1),
		Number: 1,
		Inspect: container.InspectResponse{
			State: &container.State{Status: container.StatePaused, Running: true, Paused: true},
		},
	}}
	tests := []struct {
		name          string
		introspection composemodel.IntrospectionResult
		unpauseErr    error
		downErr       error
		wantWarnings  []string
		wantDown      bool
		wantErr       string
	}{
		{name: "a running project", introspection: composetest.DemoIntrospection(), wantDown: true},
		{
			name:          "a failed down hands back no vacant project",
			introspection: composetest.DemoIntrospection(),
			downErr:       errors.New("boom"),
			wantDown:      true,
			wantErr:       "removing the compose project: boom",
		},
		{
			name:          "a frozen replica resumed first",
			introspection: paused,
			wantWarnings:  []string{"db-1 was paused: resuming it before the rollback"},
			wantDown:      true,
		},
		{
			name:          "a resume that fails goes on",
			introspection: paused,
			unpauseErr:    errors.New("boom"),
			wantWarnings:  []string{"db-1 was paused: resuming it before the rollback"},
			wantDown:      true,
		},
		{name: "an absent project", introspection: composetest.DemoAbsentIntrospection()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			control := &composetest.MockContainerControl{}
			runner := &composetest.MockComposeRunner{}
			var calls []*mock.Call
			if len(tt.wantWarnings) > 0 {
				calls = append(calls, control.On("UnpauseContainers", mock.Anything, []string{composetest.DemoContainerID}).Return(tt.unpauseErr).Once())
			}
			if tt.wantDown {
				calls = append(calls, runner.On("Down", mock.Anything, composetest.DemoTargetSpec).Return(tt.downErr).Once())
			}
			mock.InOrder(calls...)
			svc := newComposeWorkload(Deps{ComposeRunner: runner, ContainerControl: control})
			live := liveWorkload{service: svc, introspection: tt.introspection}
			recorder := &composetest.StepRecorder{}

			// Act
			vacant, err := live.Teardown(composetest.NewProgressContext(recorder), preparedSavepoint{Target: composemodel.Target{Project: composetest.DemoTargetSpec}})

			// Assert
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				assert.Nil(t, vacant)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, vacant)
			}
			assert.Equal(t, tt.wantWarnings, recorder.Warnings)
			control.AssertExpectations(t)
			runner.AssertExpectations(t)
		})
	}
}

// restoreDetailOf is the body of what building prepared restores, as summary carries it.
func restoreDetailOf(t *testing.T, summary provider.PreparedSummary) composemodel.RestoreDetail {
	t.Helper()
	var detail composemodel.RestoreDetail
	require.NoError(t, json.Unmarshal(summary.Detail, &detail))
	return detail
}
