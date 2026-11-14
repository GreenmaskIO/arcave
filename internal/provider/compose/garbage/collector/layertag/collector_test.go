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

package layertag

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/garbage"
	"github.com/greenmaskio/arcave/internal/provider/compose/garbage/collector/utils/gctest"
)

func TestCollector_Find(t *testing.T) {
	// Arrange
	const repository = "arcave/demo-db-1-postgres-16"
	var (
		unused       = gctest.RestoredLayerRef(repository, "019e843e-1650-7000-8000-000000000001")
		usedByRef    = gctest.RestoredLayerRef(repository, "019e843e-1650-7000-8000-000000000002")
		usedByID     = gctest.RestoredLayerRef("arcave/demo-db-2-postgres-16", "019e843e-1650-7000-8000-000000000002")
		lookalike    = gctest.RestoredLayerRef("arcave/demo-db-web-1", "019e843e-1650-7000-8000-000000000001")
		otherProject = gctest.RestoredLayerRef(repository, "019e843e-1650-7000-8000-000000000003")
		unowned      = gctest.RestoredLayerRef(repository, "019e843e-1650-7000-8000-000000000004")
	)
	origin := model.LayerOrigin{ArcaveProject: "app", ComposeProject: "demo"}
	ctx := context.Background()
	daemon := &mockDaemon{}
	daemon.On("ListLayerImages", mock.Anything).Return([]model.TaggedImage{
		{Ref: unused, ImageID: gctest.Digest("1"), Origin: origin},
		{Ref: usedByRef, ImageID: gctest.Digest("2"), Origin: origin},
		{Ref: usedByID, ImageID: gctest.Digest("3"), Origin: origin},
		{Ref: lookalike, ImageID: gctest.Digest("4"), Origin: model.LayerOrigin{ArcaveProject: "app", ComposeProject: "demo-db"}},
		{Ref: otherProject, ImageID: gctest.Digest("5"), Origin: model.LayerOrigin{ArcaveProject: "other", ComposeProject: "demo"}},
		{Ref: unowned, ImageID: gctest.Digest("6")},
	}, nil).Once()
	daemon.On("ListContainerImages", mock.Anything).Return([]model.ContainerImage{
		{ContainerID: "c1", Ref: usedByRef, ImageID: gctest.Digest("2")},
		{ContainerID: "c2", Ref: model.ImageRef("sha256:" + strings.Repeat("3", 64)), ImageID: gctest.Digest("3")},
	}, nil).Once()
	scope := garbage.DaemonScope{ComposeProjects: []model.DockerComposeProjectName{"demo"}}

	// Act
	stat, err := New(daemon, "app").Find(ctx, scope)

	// Assert
	require.NoError(t, err)
	got := stat.(model.DockerLayerTagStat)
	assert.Equal(t, []model.GarbageOp{
		{Action: model.GarbageActionUntag, Target: string(unused), Reason: model.GarbageReasonUnusedTag},
	}, got.Ops)
	assert.Empty(t, got.Skipped)
	assert.Empty(t, got.Unjudged)
	assert.Equal(t, 2, got.InUse)
	assert.Equal(t, 1, got.Unattributed)
	assert.Equal(t, 1, got.OtherComposeProjects, "demo-db is the arcave project's, but not this session's")
	assert.Nil(t, got.Summary().Bytes)
	daemon.AssertExpectations(t)
}

func TestCollector_Probe(t *testing.T) {
	tests := []struct {
		name    string
		pingErr error
	}{
		{name: "the daemon answers"},
		{name: "the daemon does not answer", pingErr: errors.New("connection refused")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			daemon := &mockDaemon{}
			daemon.On("Ping", mock.Anything).Return(tt.pingErr).Once()

			// Act
			err := New(daemon, "app").Probe(context.Background())

			// Assert
			assert.ErrorIs(t, err, tt.pingErr)
			daemon.AssertExpectations(t)
		})
	}
}

func TestCollector_Find_DaemonError(t *testing.T) {
	// Arrange
	daemon := &mockDaemon{}
	daemon.On("ListLayerImages", mock.Anything).Return([]model.TaggedImage(nil), errors.New("boom")).Once()

	// Act
	_, err := New(daemon, "app").Find(context.Background(), garbage.DaemonScope{})

	// Assert
	require.Error(t, err)
	daemon.AssertExpectations(t)
}
