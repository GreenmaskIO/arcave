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

package gcfinder

import (
	"context"
	"errors"
	"testing"

	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// Every action a gc carries out on the daemon has one eraser.
func TestNewDaemonErasers(t *testing.T) {
	// Act
	var got []model.GarbageAction
	for _, eraser := range newDaemonErasers(Deps{}) {
		got = append(got, eraser.Action())
	}

	// Assert
	assert.Equal(t, []model.GarbageAction{composemodel.GarbageActionUntag}, got)
}

// Every action a gc deletes in a compose project's store has one eraser.
func TestNewComposeProjectErasers(t *testing.T) {
	// Arrange
	want := []model.GarbageAction{
		composemodel.GarbageActionDeleteTree, composemodel.GarbageActionDeleteImage, composemodel.GarbageActionDeleteObject,
		composemodel.GarbageActionForget,
	}

	// Act
	var got []model.GarbageAction
	for _, eraser := range newComposeProjectErasers(Deps{}) {
		got = append(got, eraser.Action())
	}

	// Assert
	assert.Equal(t, want, got)
}

// The restored-layer collector's probe asks the daemon: one that does not answer says so, and the
// gc decides whether to leave the kind out or abort it.
func TestNewDaemonCollectors_DaemonDoesNotAnswer(t *testing.T) {
	// Arrange
	errUnreachable := errors.New("daemon unreachable")
	collectors := newDaemonCollectors(Deps{Daemon: unreachableDaemon{err: errUnreachable}})

	// Act
	var probeErr error
	for _, collector := range collectors {
		if probe, ok := collector.(interface{ Probe(context.Context) error }); ok {
			probeErr = probe.Probe(t.Context())
		}
	}

	// Assert
	require.ErrorIs(t, probeErr, errUnreachable)
}

// unreachableDaemon is a daemon whose every call fails with err.
type unreachableDaemon struct{ err error }

func (d unreachableDaemon) Ping(context.Context) error { return d.err }

func (d unreachableDaemon) ListLayerImages(context.Context) ([]composemodel.TaggedImage, error) {
	return nil, d.err
}

func (d unreachableDaemon) ListContainerImages(context.Context) ([]composemodel.ContainerImage, error) {
	return nil, d.err
}

func (d unreachableDaemon) RemoveImageRef(context.Context, model.ImageRef) error { return d.err }

// Every data kind is resolved by exactly one collector, at the level its report names.
func TestNewObjectCollectors(t *testing.T) {
	// Arrange
	type resolution struct {
		collector model.GarbageCollectorKind
		daemon    bool
	}
	want := map[model.GarbageDataKind]resolution{
		composemodel.GarbageDataKindRestoredLayers:      {collector: composemodel.GarbageCollectorKindDockerLayerTags, daemon: true},
		composemodel.GarbageDataKindSavepointData:       {collector: composemodel.GarbageCollectorKindSavepointDirectory},
		composemodel.GarbageDataKindUpstreamImages:      {collector: composemodel.GarbageCollectorKindOCIImages},
		composemodel.GarbageDataKindImageLayers:         {collector: composemodel.GarbageCollectorKindOCIBlobs},
		composemodel.GarbageDataKindDeduplicatedVolumes: {collector: composemodel.GarbageCollectorKindResticRepositories},
	}

	// Act
	got := map[model.GarbageDataKind][]resolution{}
	for _, collector := range newDaemonCollectors(Deps{}) {
		got[collector.DataKind()] = append(got[collector.DataKind()], resolution{collector: collector.Kind(), daemon: true})
	}
	for _, collector := range newComposeProjectCollectors(Deps{}) {
		got[collector.DataKind()] = append(got[collector.DataKind()], resolution{collector: collector.Kind()})
	}

	// Assert
	require.Len(t, want, len(composemodel.GarbageDataKinds()), "the table names every data kind of the provider")
	for dataKind, resolved := range want {
		assert.Equal(t, []resolution{resolved}, got[dataKind], dataKind)
	}
	assert.Len(t, got, len(want), "no collector resolves a data kind arcave does not know")
	var collectorKinds []model.GarbageCollectorKind
	for _, resolved := range got {
		for _, resolution := range resolved {
			collectorKinds = append(collectorKinds, resolution.collector)
		}
	}
	assert.ElementsMatch(t,
		composemodel.GarbageCollectorKinds(), collectorKinds, "every collector kind is built once")
}
