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

package capturepolicy

import (
	"context"
	"maps"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// defaults capture every kind, by the default backends.
var defaults = CaptureDefaults{
	VolumeBackend:        model.SnapshotBackendFullCopy,
	WritableLayerBackend: model.ImageBackendDedupCopy,
	SavepointCapture: SavepointCaptureSettings{
		WritableLayers:   true,
		Images:           true,
		NamedVolumes:     true,
		BindVolumes:      true,
		AnonymousVolumes: true,
	},
}

// demoProject is app with a bind and an anonymous volume, db with a named
// volume, and labels put on each as the case says.
func demoProject(appLabels, dbLabels, anonLabels, pgdataLabels map[string]string) model.IntrospectionResult {
	return model.IntrospectionResult{Project: model.Project{
		Name: "demo",
		Services: []model.Service{
			{Name: "app", Labels: appLabels, Volumes: []model.ServiceVolume{
				{Type: model.VolumeTypeBind, Source: "/src", Target: "/app"},
				{Type: model.VolumeTypeAnon, Target: "/cache", Replica: 1, Volume: &model.VolumeMount{Labels: anonLabels}},
				{Type: model.VolumeTypeTmpfs, Target: "/tmp"},
			}},
			{Name: "db", Labels: dbLabels, Volumes: []model.ServiceVolume{
				{Type: model.VolumeTypeNamed, Source: "pgdata", Target: "/var/lib/postgresql/data", VolumeName: "demo_pgdata"},
			}},
		},
		Volumes: []model.NamedVolume{{
			Key: "pgdata", Name: "demo_pgdata", Labels: pgdataLabels,
			UsedBy: []model.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}},
		}},
	}}
}

var (
	appBind = model.MountRef{Service: "app", Target: "/app"}
	appAnon = model.MountRef{Service: "app", Replica: 1, Target: "/cache"}
)

func nopCtx() context.Context { return zerolog.Nop().WithContext(context.Background()) }

// byConfig is a volume captured by the defaults.
func byConfig(backend model.SnapshotBackendKind) model.VolumeDecision {
	return model.VolumeDecision{CaptureDecision: model.NewConfigDecision(true, ""), Backend: backend}
}

// byLabel is a decision a label made.
func byLabel(capture bool, source string) model.CaptureDecision {
	return model.CaptureDecision{Capture: capture, Origin: model.OriginLabel, Source: source}
}

// defaultImages is a service's images decided by the defaults.
func defaultImages() model.ServiceDecision {
	return model.CapturePolicy{WritableLayerBackend: model.ImageBackendDedupCopy}.DecideService("any")
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name                         string
		app, db, anon, pgdata        map[string]string
		wantBind, wantAnon, wantData model.VolumeDecision
		wantApp, wantDB              model.ServiceDecision
	}{
		{
			name:     "no label captures everything by the defaults",
			wantBind: byConfig(model.SnapshotBackendFullCopy),
			wantAnon: byConfig(model.SnapshotBackendFullCopy),
			wantData: byConfig(model.SnapshotBackendFullCopy),
			wantApp:  defaultImages(),
			wantDB:   defaultImages(),
		},
		{
			name: "a service left out takes its layer, upstream and own mounts, and a volume only it mounts",
			app:  map[string]string{model.LabelCapture: "false"},
			db:   map[string]string{model.LabelCapture: "false"},
			wantBind: model.VolumeDecision{Backend: model.SnapshotBackendFullCopy,
				CaptureDecision: byLabel(false, "arcave.capture=false on service app")},
			wantAnon: model.VolumeDecision{Backend: model.SnapshotBackendFullCopy,
				CaptureDecision: byLabel(false, "arcave.capture=false on service app")},
			wantData: model.VolumeDecision{Backend: model.SnapshotBackendFullCopy,
				CaptureDecision: byLabel(false, "arcave.capture=false on every service mounting it")},
			wantApp: model.ServiceDecision{
				WritableLayer: model.WritableLayerDecision{Backend: model.ImageBackendDedupCopy,
					CaptureDecision: byLabel(false, "arcave.capture=false on service app")},
				Image: byLabel(false, "arcave.capture=false on service app"),
			},
			wantDB: model.ServiceDecision{
				WritableLayer: model.WritableLayerDecision{Backend: model.ImageBackendDedupCopy,
					CaptureDecision: byLabel(false, "arcave.capture=false on service db")},
				Image: byLabel(false, "arcave.capture=false on service db"),
			},
		},
		{
			name: "the most specific label wins over the service's",
			app: map[string]string{
				model.LabelCapture:              "false",
				model.LabelWritableLayerCapture: "true",
				model.LabelWritableLayerBackend: "fullcopy",
				model.LabelVolumeBackend:        "cdc",
			},
			anon:   map[string]string{model.LabelCapture: "true", model.LabelBackend: "fullcopy"},
			pgdata: map[string]string{model.LabelBackend: "cdc"},
			wantBind: model.VolumeDecision{Backend: model.SnapshotBackendCDC,
				CaptureDecision: byLabel(false, "arcave.capture=false on service app")},
			wantAnon: model.VolumeDecision{Backend: model.SnapshotBackendFullCopy,
				CaptureDecision: byLabel(true, "arcave.capture=true on the anonymous volume at /cache of service app")},
			wantData: model.VolumeDecision{Backend: model.SnapshotBackendCDC,
				CaptureDecision: model.NewConfigDecision(true, "")},
			wantApp: model.ServiceDecision{
				WritableLayer: model.WritableLayerDecision{Backend: model.ImageBackendFullCopy,
					CaptureDecision: byLabel(true, "arcave.writable-layer.capture=true on service app")},
				Image: byLabel(false, "arcave.capture=false on service app"),
			},
			wantDB: defaultImages(),
		},
		{
			name: "target lists exclude a mount and pick another's backend",
			app: map[string]string{
				model.LabelMountsExclude: "/app",
				model.LabelMountsCDC:     " /cache ,",
				model.LabelImageCapture:  "false",
			},
			wantBind: model.VolumeDecision{Backend: model.SnapshotBackendFullCopy,
				CaptureDecision: byLabel(false, "arcave.mounts.exclude=/app on service app")},
			wantAnon: model.VolumeDecision{Backend: model.SnapshotBackendCDC,
				CaptureDecision: model.NewConfigDecision(true, "")},
			wantData: byConfig(model.SnapshotBackendFullCopy),
			wantApp: model.ServiceDecision{
				WritableLayer: defaultImages().WritableLayer,
				Image:         byLabel(false, "arcave.image.capture=false on service app"),
			},
			wantDB: defaultImages(),
		},
		{
			name: "a target is read as a clean path",
			app: map[string]string{
				model.LabelMountsExclude: "/app/",
				model.LabelMountsCDC:     "/cache/.",
			},
			wantBind: model.VolumeDecision{Backend: model.SnapshotBackendFullCopy,
				CaptureDecision: byLabel(false, "arcave.mounts.exclude=/app/ on service app")},
			wantAnon: model.VolumeDecision{Backend: model.SnapshotBackendCDC,
				CaptureDecision: model.NewConfigDecision(true, "")},
			wantData: byConfig(model.SnapshotBackendFullCopy),
			wantApp:  defaultImages(),
			wantDB:   defaultImages(),
		},
		{
			name:     "a named volume's own label leaves it out whatever its services say",
			pgdata:   map[string]string{model.LabelCapture: "false"},
			wantBind: byConfig(model.SnapshotBackendFullCopy),
			wantAnon: byConfig(model.SnapshotBackendFullCopy),
			wantData: model.VolumeDecision{Backend: model.SnapshotBackendFullCopy,
				CaptureDecision: byLabel(false, "arcave.capture=false on volume demo_pgdata")},
			wantApp: defaultImages(),
			wantDB:  defaultImages(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			introspection := demoProject(tt.app, tt.db, tt.anon, tt.pgdata)

			// Act
			policy, err := Resolve(nopCtx(), introspection, defaults)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.wantBind, policy.DecideMount(appBind), "bind")
			assert.Equal(t, tt.wantAnon, policy.DecideMount(appAnon), "anonymous volume")
			assert.Equal(t, tt.wantData, policy.DecideNamedVolume("demo_pgdata"), "named volume")
			assert.Equal(t, tt.wantApp, policy.DecideService("app"), "app")
			assert.Equal(t, tt.wantDB, policy.DecideService("db"), "db")
		})
	}
}

// A named volume one excluded service and one captured service share is captured.
func TestResolve_SharedNamedVolumeStaysWhileOneServiceIsCaptured(t *testing.T) {
	// Arrange
	introspection := demoProject(map[string]string{model.LabelCapture: "false"}, nil, nil, nil)
	introspection.Project.Volumes[0].UsedBy = append(introspection.Project.Volumes[0].UsedBy,
		model.ServiceReference{Service: "app", Target: "/shared"})

	// Act
	policy, err := Resolve(nopCtx(), introspection, defaults)

	// Assert
	require.NoError(t, err)
	assert.True(t, policy.DecideNamedVolume("demo_pgdata").Capture)
}

func TestResolve_Refuses(t *testing.T) {
	tests := []struct {
		name          string
		app, anon, pg map[string]string
		wantMsg       string
	}{
		{name: "an unknown key", app: map[string]string{"arcave.captur": "false"}, wantMsg: `arcave.captur="false" on service app`},
		{name: "a value not a bool", app: map[string]string{model.LabelCapture: "no"}, wantMsg: "want true or false"},
		{name: "an unknown volume backend", app: map[string]string{model.LabelVolumeBackend: "rsync"}, wantMsg: "want fullcopy or cdc"},
		{name: "an unknown image backend", app: map[string]string{model.LabelWritableLayerBackend: "copy"}, wantMsg: "want dedupcopy or fullcopy"},
		{name: "a service label on a volume", pg: map[string]string{model.LabelWritableLayerCapture: "false"}, wantMsg: "on volume demo_pgdata"},
		{name: "a volume label on a service", app: map[string]string{model.LabelBackend: "cdc"}, wantMsg: "not a label arcave reads here"},
		{name: "a per-mount label", app: map[string]string{"arcave.mount./app.capture": "false"}, wantMsg: "not a label arcave reads here"},
		{name: "a bad anonymous volume label", anon: map[string]string{model.LabelBackend: "x"}, wantMsg: "anonymous volume at /cache"},
		{name: "a target mounting nothing", app: map[string]string{model.LabelMountsExclude: "/nope"}, wantMsg: "mounts nothing at /nope"},
		{name: "a target of a tmpfs", app: map[string]string{model.LabelMountsExclude: "/tmp"}, wantMsg: "not a volume or bind of the service's own"},
		{name: "a target in both backend lists", app: map[string]string{
			model.LabelMountsCDC: "/app", model.LabelMountsFullCopy: "/app"}, wantMsg: "listed for both"},
		{name: "a target both included and excluded", app: map[string]string{
			model.LabelMountsInclude: "/app", model.LabelMountsExclude: "/app"}, wantMsg: "also in arcave.mounts.exclude"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			introspection := demoProject(tt.app, nil, tt.anon, tt.pg)

			// Act
			_, err := Resolve(nopCtx(), introspection, defaults)

			// Assert
			require.ErrorIs(t, err, model.ErrInvalidCapturePolicy)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}

// A service's labels reach the named volume it mounts, so an existing volume never
// needs a label of its own.
func TestResolve_ServiceLabelsReachANamedVolume(t *testing.T) {
	tests := []struct {
		name     string
		db       map[string]string
		shares   map[string]string
		namedOff bool // the config leaves named volumes out
		want     model.VolumeDecision
		wantErr  string
	}{
		{
			name: "excluded",
			db:   map[string]string{model.LabelMountsExclude: "/var/lib/postgresql/data"},
			want: model.VolumeDecision{Backend: model.SnapshotBackendFullCopy,
				CaptureDecision: byLabel(false, "arcave.mounts.exclude=/var/lib/postgresql/data on service db")},
		},
		{
			name:   "one service's exclusion wins over another's inclusion",
			db:     map[string]string{model.LabelMountsInclude: "/var/lib/postgresql/data"},
			shares: map[string]string{model.LabelMountsExclude: "/shared"},
			want: model.VolumeDecision{Backend: model.SnapshotBackendFullCopy,
				CaptureDecision: byLabel(false, "arcave.mounts.exclude=/shared on service app")},
		},
		{
			name: "its backend picked",
			db:   map[string]string{model.LabelMountsCDC: "/var/lib/postgresql/data"},
			want: model.VolumeDecision{Backend: model.SnapshotBackendCDC,
				CaptureDecision: model.NewConfigDecision(true, "")},
		},
		{
			name:     "a service's arcave.capture=true takes it back from the config",
			db:       map[string]string{model.LabelCapture: "true"},
			namedOff: true,
			want: model.VolumeDecision{Backend: model.SnapshotBackendFullCopy,
				CaptureDecision: byLabel(true, "arcave.capture=true on service db")},
		},
		{
			name:    "two services naming different backends are refused",
			db:      map[string]string{model.LabelMountsCDC: "/var/lib/postgresql/data"},
			shares:  map[string]string{model.LabelMountsFullCopy: "/shared"},
			wantErr: "services app and db name different backends for volume demo_pgdata",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: app mounts pgdata too, at /shared.
			introspection := demoProject(tt.shares, tt.db, nil, nil)
			introspection.Project.Services[0].Volumes = append(introspection.Project.Services[0].Volumes, model.ServiceVolume{
				Type: model.VolumeTypeNamed, Source: "pgdata", Target: "/shared", VolumeName: "demo_pgdata"})
			introspection.Project.Volumes[0].UsedBy = []model.ServiceReference{
				{Service: "app", Target: "/shared"}, {Service: "db", Target: "/var/lib/postgresql/data"}}
			config := defaults
			config.SavepointCapture.NamedVolumes = !tt.namedOff

			// Act
			policy, err := Resolve(nopCtx(), introspection, config)

			// Assert
			if tt.wantErr != "" {
				require.ErrorIs(t, err, model.ErrInvalidCapturePolicy)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, policy.DecideNamedVolume("demo_pgdata"))
		})
	}
}

// The config leaves a kind out; a label on the unit or its service takes it back.
func TestResolve_ConfigKinds(t *testing.T) {
	// Arrange
	captureNothing := defaults
	captureNothing.SavepointCapture = SavepointCaptureSettings{}
	introspection := demoProject(
		map[string]string{model.LabelMountsInclude: "/app", model.LabelWritableLayerCapture: "true"},
		nil, nil, nil)

	// Act
	policy, err := Resolve(nopCtx(), introspection, captureNothing)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, byLabel(true, "arcave.mounts.include=/app on service app"),
		policy.DecideMount(appBind).CaptureDecision, "included back by its service")
	assert.Equal(t, model.NewConfigDecision(false, "capture.volumes.anonymous=false in the config"), policy.DecideMount(appAnon).CaptureDecision)
	assert.Equal(t, model.NewConfigDecision(false, "capture.volumes.named=false in the config"), policy.DecideNamedVolume("demo_pgdata").CaptureDecision)
	assert.True(t, policy.DecideService("app").WritableLayer.Capture, "taken back by its label")
	assert.Equal(t, model.NewConfigDecision(false, "capture.writable_layers=false in the config"), policy.DecideService("db").WritableLayer.CaptureDecision)
	assert.Equal(t, model.NewConfigDecision(false, "capture.images=false in the config"), policy.DecideService("app").Image)
}

// Labels outside arcave.* are none of the policy's business, and an unknown
// arcave.* key the image itself carries is the image's, not a typo.
func TestResolve_IgnoresOtherLabels(t *testing.T) {
	// Arrange
	labels := map[string]string{
		"com.docker.compose.project": "demo",
		model.BaseImageIDLabel:       "sha256:x",
		"org.opencontainers.title":   "app",
		"arcave.version":             "1",
	}
	introspection := demoProject(labels, nil, nil, map[string]string{"com.docker.compose.project": "demo"})
	imageID := model.ImageDigestID("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	introspection.Project.Services[0].Containers = []model.Container{{Inspect: container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{Image: imageID.String()},
	}}}
	introspection.Images = map[model.ImageDigestID]image.InspectResponse{
		imageID: {Config: &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{Labels: map[string]string{"arcave.version": "1"}}}},
	}

	// Act
	_, err := Resolve(nopCtx(), introspection, defaults)

	// Assert
	require.NoError(t, err)
}

// With every label ignored, the config still decides.
func TestResolveWithoutLabels(t *testing.T) {
	// Arrange
	noBinds := defaults
	noBinds.SavepointCapture.BindVolumes = false
	introspection := demoProject(map[string]string{"arcave.captur": "false"}, nil, map[string]string{model.LabelBackend: "x"}, nil)

	// Act
	policy, err := ResolveWithoutLabels(nopCtx(), introspection, noBinds)

	// Assert
	require.NoError(t, err)
	assert.False(t, policy.DecideMount(appBind).Capture)
	assert.True(t, policy.DecideMount(appAnon).Capture)
}

// Every decision is stamped on the services, with its origin and source; a named
// volume's on each service mounting it.
func TestStamps(t *testing.T) {
	// Arrange
	noBinds := defaults
	noBinds.SavepointCapture.BindVolumes = false
	introspection := demoProject(map[string]string{model.LabelMountsCDC: "/cache"}, nil, nil, nil)
	policy, err := Resolve(nopCtx(), introspection, noBinds)
	require.NoError(t, err)

	// Act
	got := Stamps(introspection, policy)

	// Assert
	assert.Equal(t, map[string]string{
		"io.arcave.capture.writable-layer": "true; backend=dedupcopy; origin=config",
		"io.arcave.capture.image":          "true; origin=config",
		"io.arcave.capture.mount./app":     "false; origin=config; source=capture.volumes.bind=false in the config",
		"io.arcave.capture.mount./cache":   "true; backend=cdc; origin=config",
	}, got["app"])
	assert.Equal(t, "true; backend=fullcopy; origin=config", got["db"][model.MountStamp("/var/lib/postgresql/data")])
}

// A container a rollback builds carries the stamps beside the user's labels; the
// stamps are never read, so the user's labels alone decide again.
func TestStamps_AreNotReadBack(t *testing.T) {
	// Arrange
	user := map[string]string{model.LabelMountsExclude: "/app", model.LabelWritableLayerBackend: "fullcopy"}
	introspection := demoProject(user, nil, nil, nil)
	policy, err := Resolve(nopCtx(), introspection, defaults)
	require.NoError(t, err)
	rebuilt := maps.Clone(user)
	maps.Copy(rebuilt, Stamps(introspection, policy)["app"])

	// Act
	again, err := Resolve(nopCtx(), demoProject(rebuilt, nil, nil, nil), defaults)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, policy, again)
}
