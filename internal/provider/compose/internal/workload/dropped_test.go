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
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
)

// The tags of this arcave project's compose project come off, of no other origin; a tag that will
// not come off is a notice, one already gone nothing.
func TestDropped_RemoveRestoredLayers(t *testing.T) {
	tag := func(n string) model.ImageRef { return model.ImageRef("arcave/demo-db-1-postgres:sp-" + n) }
	origin := composemodel.LayerOrigin{ArcaveProject: composetest.DemoArcaveProject, ComposeProject: composetest.DemoProject}

	t.Run("only this compose project's", func(t *testing.T) {
		// Arrange
		svc, doubles := newDropWorkload(Deps{})
		surveyed := survey(t, svc, doubles, composemodel.ComposeProjectResources{})
		ctx := zerolog.Nop().WithContext(context.Background())
		dropped, err := surveyed.Drop(ctx)
		require.NoError(t, err)
		mock.InOrder(
			doubles.tags.On("ListLayerImages", mock.Anything).Return([]composemodel.TaggedImage{
				{Ref: tag("1"), Origin: origin},
				{Ref: tag("2"), Origin: composemodel.LayerOrigin{ArcaveProject: composetest.DemoArcaveProject, ComposeProject: "other"}},
				{Ref: tag("3"), Origin: composemodel.LayerOrigin{ArcaveProject: "blog", ComposeProject: composetest.DemoProject}},
				{Ref: tag("4")},
				{Ref: tag("5"), Origin: origin},
				{Ref: tag("6"), Origin: origin},
			}, nil).Once(),
			doubles.tags.On("RemoveImageRef", mock.Anything, tag("1")).Return(nil).Once(),
			doubles.tags.On("RemoveImageRef", mock.Anything, tag("5")).
				Return(fmt.Errorf("remove image reference: %w", composemodel.ErrImageRefNotFound)).Once(),
			doubles.tags.On("RemoveImageRef", mock.Anything, tag("6")).
				Return(errors.New("image is used by container c0ffee")).Once(),
		)

		// Act
		removed, notices := dropped.RemoveRestoredLayers(ctx)

		// Assert
		assert.Equal(t, []model.ImageRef{tag("1")}, removed)
		require.Len(t, notices, 1)
		assert.Contains(t, notices[0].Message, string(tag("6")))
		doubles.assert(t)
	})

	t.Run("an unreadable listing is a notice", func(t *testing.T) {
		// Arrange
		svc, doubles := newDropWorkload(Deps{})
		surveyed := survey(t, svc, doubles, composemodel.ComposeProjectResources{})
		ctx := zerolog.Nop().WithContext(context.Background())
		dropped, err := surveyed.Drop(ctx)
		require.NoError(t, err)
		doubles.tags.On("ListLayerImages", mock.Anything).Return(nil, errors.New("daemon gone")).Once()

		// Act
		removed, notices := dropped.RemoveRestoredLayers(ctx)

		// Assert
		assert.Equal(t, []model.ImageRef{}, removed)
		require.Len(t, notices, 1)
		assert.Contains(t, notices[0].Message, "daemon gone")
		doubles.assert(t)
	})
}

// The dropped compose project is built on as one that owned nothing and orphaned nothing; a
// savepoint another provider prepared is refused.
func TestDropped_Vacant(t *testing.T) {
	// Arrange
	svc, doubles := newDropWorkload(Deps{})
	surveyed := survey(t, svc, doubles, composemodel.ComposeProjectResources{})
	dropped, err := surveyed.Drop(zerolog.Nop().WithContext(context.Background()))
	require.NoError(t, err)

	// Act
	vacant, err := dropped.Vacant(preparedSavepoint{Target: composemodel.Target{Project: composetest.DemoTargetSpec}})
	_, foreignErr := dropped.Vacant(foreignPrepared{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, vacantWorkload{
		service:              svc,
		anonymousVolumeNames: map[string]string{},
		ownedBefore:          map[string]bool{},
		downProject:          composetest.DemoTargetSpec,
	}, vacant)
	require.ErrorIs(t, foreignErr, errForeignPrepared)
}

// foreignPrepared is a prepared savepoint no compose provider handed out.
type foreignPrepared struct{}

func (foreignPrepared) Summary() provider.PreparedSummary { return provider.PreparedSummary{} }
