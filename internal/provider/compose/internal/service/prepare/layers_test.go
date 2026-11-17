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

package prepare

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/utils/composetest"
)

func TestRestoredLayerTag(t *testing.T) {
	const id = model.SavepointID("019e843e-1650-7000-8000-000000000000")
	// A repository path may hold at most 255 characters, and a path component only
	// lower-case alphanumerics joined by separators — neither is arcave's to widen.
	longImage := strings.Repeat("x", 300)

	tests := []struct {
		name    string
		project string
		capture model.WritableLayer
		want    string
	}{
		{
			name:    "project, service, replica and upstream image",
			project: "demo",
			capture: model.WritableLayer{Service: "db", Replica: 1, UpstreamImageRef: "postgres:16"},
			want:    "arcave/demo-db-1-postgres-16:sp-019e843e-1650-7000-8000-000000000000",
		},
		{
			name:    "a registry-qualified upstream image",
			project: "demo",
			capture: model.WritableLayer{Service: "web", Replica: 2, UpstreamImageRef: "ghcr.io/Org/App:1.2.3"},
			want:    "arcave/demo-web-2-ghcr-io-org-app-1-2-3:sp-019e843e-1650-7000-8000-000000000000",
		},
		{
			name:    "an upper-case service, which a repository path refuses",
			project: "Demo_App",
			capture: model.WritableLayer{Service: "DB", Replica: 1, UpstreamImageRef: "busybox"},
			want:    "arcave/demo-app-db-1-busybox:sp-019e843e-1650-7000-8000-000000000000",
		},
		{
			name:    "a capture whose upstream could not be traced is still addressable",
			project: "demo",
			capture: model.WritableLayer{Service: "db", Replica: 1},
			want:    "arcave/demo-db-1:sp-019e843e-1650-7000-8000-000000000000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := restoredLayerTag(tt.project, id, tt.capture)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("an upstream image too long for the cap is cut, never the identity", func(t *testing.T) {
		// Act
		got := restoredLayerTag("demo", id, model.WritableLayer{
			Service: "db", Replica: 1, UpstreamImageRef: longImage,
		})

		// Assert
		repository, tag, _ := strings.Cut(got, ":")
		assert.Len(t, repository, 255)
		assert.True(t, strings.HasPrefix(repository, "arcave/demo-db-1-"), repository)
		assert.Equal(t, "sp-019e843e-1650-7000-8000-000000000000", tag)
	})
}

// TestRestoredLayerTag_IsAlwaysNameable pins the ref against docker's own path
// grammar: lower-case alphanumeric runs joined by single dashes is a subset of it,
// so no service or image name can produce something the daemon refuses to tag.
func TestRestoredLayerTag_IsAlwaysNameable(t *testing.T) {
	nameable := regexp.MustCompile(`^arcave/[a-z0-9]+(-[a-z0-9]+)*:sp-[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

	names := []string{"", "___", "..", "-lead", "trail-", "UPPER", "a b/c:d@e", "юникод"}
	bases := append(append([]string{}, names...), strings.Repeat("x", 300))
	for _, project := range names {
		for _, service := range names {
			for _, base := range bases {
				// Act
				got := restoredLayerTag(project, "019e843e-1650-7000-8000-000000000000", model.WritableLayer{
					Service: service, Replica: 1, UpstreamImageRef: base,
				})

				// Assert
				assert.Regexp(t, nameable, got, "project %q, service %q, base image %q", project, service, base)
				repository, _, _ := strings.Cut(got, ":")
				assert.LessOrEqual(t, len(repository), 255)
			}
		}
	}
}

// The service's one image is its lowest recorded replica's, whether or not that is 1.
func TestRetargetImages_LowestRecordedReplica(t *testing.T) {
	// Arrange
	project := &types.Project{Name: composetest.DemoProject, Services: types.Services{
		composetest.DemoService: {Name: composetest.DemoService, Image: composetest.DemoBaseImage},
	}}
	restored := []model.WritableLayerRestoreResult{
		{Service: composetest.DemoService, Replica: 3, Tag: composetest.DemoRestoredTag(3)},
		{Service: composetest.DemoService, Replica: 2, Tag: composetest.DemoRestoredTag(2)},
	}

	// Act
	got := retargetImages(project, restored)

	// Assert
	assert.Equal(t, composetest.DemoRestoredTag(2), got.Services[composetest.DemoService].Image)
	assert.Equal(t, types.PullPolicyNever, got.Services[composetest.DemoService].PullPolicy)
	assert.Equal(t, composetest.DemoBaseImage, project.Services[composetest.DemoService].Image, "the input project is left alone")
}

func TestPreparer_requireImages(t *testing.T) {
	tests := []struct {
		name     string
		policy   string
		restored []model.WritableLayerRestoreResult
		present  *bool
		wantErr  bool
	}{
		{name: "an image compose may pull is not looked up", policy: types.PullPolicyMissing},
		{name: "a service on a restored layer is not looked up", policy: types.PullPolicyNever,
			restored: []model.WritableLayerRestoreResult{{Service: "db"}}},
		{name: "a local image the daemon holds passes", policy: types.PullPolicyNever, present: boolPtr(true)},
		{name: "a local image the daemon lacks is refused", policy: types.PullPolicyNever, present: boolPtr(false), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			project := &types.Project{Services: types.Services{"db": {Name: "db", Image: "local:1", PullPolicy: tt.policy}}}
			lookup := &composetest.MockImageLookup{}
			if tt.present != nil {
				lookup.On("ImageRefExists", mock.Anything, "local:1").Return(*tt.present, nil).Once()
			}
			svc := New(Deps{ImageLookup: lookup})

			// Act
			err := svc.requireImages(context.Background(), project, tt.restored)

			// Assert
			if tt.wantErr {
				require.ErrorIs(t, err, errImageUnavailable)
			} else {
				require.NoError(t, err)
			}
			lookup.AssertExpectations(t)
		})
	}
}

func boolPtr(v bool) *bool { return &v }
