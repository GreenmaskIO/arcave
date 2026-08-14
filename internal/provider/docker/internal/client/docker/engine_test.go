package docker

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
)

func TestMatchMount(t *testing.T) {
	summaries := []container.Summary{
		{
			ID: "other", Mounts: []container.MountPoint{
				{
					Type:        mount.TypeVolume,
					Name:        "demo_other",
					Destination: "/elsewhere",
				},
			}},
		{
			ID: "target", Mounts: []container.MountPoint{
				{
					Type:        mount.TypeVolume,
					Name:        "demo_pgdata",
					Destination: "/var/lib/postgresql/data",
				},
				{
					Type:        mount.TypeBind,
					Source:      "/host/cfg",
					Destination: "/etc/cfg",
				},
			}},
	}

	tests := []struct {
		name    string
		target  string
		wantOK  bool
		wantID  string
		wantMnt mountInfo
	}{
		{
			name:    "matches volume mount",
			target:  "/var/lib/postgresql/data",
			wantOK:  true,
			wantID:  "target",
			wantMnt: mountInfo{Type: "volume", Name: "demo_pgdata"},
		},
		{
			name:    "matches bind mount",
			target:  "/etc/cfg",
			wantOK:  true,
			wantID:  "target",
			wantMnt: mountInfo{Type: "bind", Source: "/host/cfg"},
		},
		{
			name:   "no container mounts target",
			target: "/nope",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			id, mnt, ok := matchMount(summaries, tt.target)

			// Assert
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantID, id)
				assert.Equal(t, tt.wantMnt, mnt)
			}
		})
	}
}

// Engine's own methods run against the api seam, so they are exercised with a
// mocked arcaved — the coverage that the field being an injected interface buys.
//
// What the compose project already answers is never asked of the arcaved: a
// named volume and a bind address themselves, with no container listing at all,
// which is what lets a savepoint cover a volume no container mounts.
func TestEngine_ResolveMountSource(t *testing.T) {
	const target = "/var/lib/postgresql/data"

	tests := []struct {
		name    string
		volume  dockermodel.Volume
		arrange func(api *mockAPI)
		want    dockermodel.MountSource
		wantErr string
	}{
		{
			name:   "a named volume is its docker name, and no container is looked for",
			volume: namedVolume("demo_pgdata"),
			arrange: func(api *mockAPI) {
				api.On("VolumeInspect", mock.Anything, "demo_pgdata").
					Return(volume.Volume{Name: "demo_pgdata"}, nil).Once()
			},
			want: dockermodel.MountSource{Type: dockermodel.MountSourceVolume, Source: "demo_pgdata"},
		},
		{
			name: "a bind is its host path, and the arcaved is not asked at all",
			volume: dockermodel.Volume{
				ObjectID: "volume-service-app-etc-cfg", ComposeProject: "demo", Type: dockermodel.VolumeTypeBind,
				Bind: &dockermodel.VolumePropertiesBind{Service: "app", Target: "/etc/cfg", Source: "/host/cfg"},
			},
			want: dockermodel.MountSource{Type: dockermodel.MountSourceBind, Source: "/host/cfg"},
		},
		{
			name:   "a named volume the arcaved does not have is refused before anything creates it",
			volume: namedVolume("demo_pgdata"),
			arrange: func(api *mockAPI) {
				api.On("VolumeInspect", mock.Anything, "demo_pgdata").
					Return(volume.Volume{}, errors.New("no such volume")).Once()
			},
			wantErr: "does not exist",
		},
		{
			name:   "an anonymous volume is looked up through the container holding it",
			volume: anonVolume("db", target),
			arrange: func(api *mockAPI) {
				api.On("ContainerList", mock.Anything, matchComposeLabels("demo", "db")).
					Return([]container.Summary{{ID: "pg-1", Mounts: []container.MountPoint{
						{Type: mount.TypeVolume, Name: "9f2c0a", Destination: target}}}}, nil).Once()
			},
			want: dockermodel.MountSource{Type: dockermodel.MountSourceVolume, Source: "9f2c0a"},
		},
		{
			name: "a bind whose source never resolved falls back to the same lookup",
			volume: dockermodel.Volume{
				ObjectID: "volume-service-app-etc-cfg", ComposeProject: "demo", Type: dockermodel.VolumeTypeBind,
				Bind: &dockermodel.VolumePropertiesBind{Service: "app", Target: "/etc/cfg", Source: "./cfg"},
			},
			arrange: func(api *mockAPI) {
				api.On("ContainerList", mock.Anything, matchComposeLabels("demo", "app")).
					Return([]container.Summary{{ID: "app-1", Mounts: []container.MountPoint{
						{Type: mount.TypeBind, Source: "/host/cfg", Destination: "/etc/cfg"}}}}, nil).Once()
			},
			want: dockermodel.MountSource{Type: dockermodel.MountSourceBind, Source: "/host/cfg"},
		},
		{
			name:   "an anonymous volume with no container left names the service it needed",
			volume: anonVolume("db", target),
			arrange: func(api *mockAPI) {
				api.On("ContainerList", mock.Anything, matchComposeLabels("demo", "db")).
					Return([]container.Summary{}, nil).Once()
			},
			wantErr: "no container for service",
		},
		{
			name:    "a volume carrying no way to reach its data",
			volume:  dockermodel.Volume{ObjectID: "volume-named-broken", ComposeProject: "demo", Type: dockermodel.VolumeTypeNamed},
			wantErr: "no way to reach its data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: no ContainerCreate expectation is ever declared, so creating
			// anything — the volume a failed inspect just refused, above all — fails
			// the run where it happened.
			api := &mockAPI{}
			if tt.arrange != nil {
				tt.arrange(api)
			}
			e := &Engine{api: api}

			// Act
			got, err := e.ResolveMountSource(context.Background(), tt.volume)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Equal(t, dockermodel.MountSource{}, got)
				api.AssertExpectations(t)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			api.AssertExpectations(t)
		})
	}
}

// The whole volume comes out through a container of arcave's own, mounted at
// scratchMount — no service container is involved. The container outlives the
// call: it goes away only once the caller closes the stream copied out of it.
func TestEngine_OpenVolume(t *testing.T) {
	// Arrange
	const payload = "tar-bytes"
	api := &mockAPI{}
	expectImagePresent(api, scratchImage)
	scratch := expectContainerRun(api, "scratch-1", scratchCmd[0])
	copyOut := api.On("CopyFromContainer", mock.Anything, "scratch-1", scratchMount).
		Return(io.NopCloser(strings.NewReader(payload)), container.PathStat{}, nil).Once()
	removed := make(chan struct{})
	scratch.remove.Run(func(mock.Arguments) { close(removed) })
	mock.InOrder(scratch.create, copyOut, scratch.remove)
	e := &Engine{api: api}

	// Act
	rc, err := e.OpenVolume(context.Background(), dockermodel.MountSource{
		Type: dockermodel.MountSourceVolume, Source: "demo_pgdata"})

	// Assert
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, payload, string(got))
	assertNotYet(t, removed, "the container must outlive the stream copied out of it")

	require.NoError(t, rc.Close())
	assertClosed(t, removed, "closing the stream must remove the container")
	api.AssertExpectations(t)
	assert.Equal(t, []string{"demo_pgdata:" + scratchMount}, scratch.host.Binds)
}

// A restore clears the volume before extracting into it, and extracts at the
// container's root: the archive's top entry is scratchMount's own basename, so
// no path arithmetic decides where the data lands.
func TestEngine_WriteVolume(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	expectImagePresent(api, scratchImage)
	wipe := expectContainerRun(api, "wipe-1", "find")
	scratch := expectContainerRun(api, "scratch-1", scratchCmd[0])
	extract := api.On("CopyToContainer", mock.Anything, "scratch-1", "/", mock.Anything, mock.Anything).
		Return(nil).Once()
	// The wipe must be finished before a byte is written back, or the restore
	// deletes itself.
	mock.InOrder(wipe.remove, scratch.create, extract, scratch.remove)
	e := &Engine{api: api}

	// Act
	err := e.WriteVolume(context.Background(),
		dockermodel.MountSource{Type: dockermodel.MountSourceVolume, Source: "demo_pgdata"},
		strings.NewReader("payload"))

	// Assert
	require.NoError(t, err)
	api.AssertExpectations(t)
	assert.Equal(t, []string{"demo_pgdata:" + wipeMountPath}, wipe.host.Binds, "the wipe binds the volume it clears")
	assert.Equal(t, []string{"demo_pgdata:" + scratchMount}, scratch.host.Binds)
}

// A volume the arcaved refuses to clear stops the restore before the stream is
// extracted: half-wiped is worse than untouched.
func TestEngine_WriteVolume_AFailedWipeWritesNothing(t *testing.T) {
	// Arrange: no CopyToContainer expectation, so an extract fails the run.
	api := &mockAPI{}
	expectImagePresent(api, scratchImage)
	expectContainerRun(api, "wipe-1", "find").exit(1)
	api.On("ContainerLogs", mock.Anything, "wipe-1", mock.Anything).
		Return(io.NopCloser(strings.NewReader("")), nil).Once()
	e := &Engine{api: api}

	// Act
	err := e.WriteVolume(context.Background(),
		dockermodel.MountSource{Type: dockermodel.MountSourceVolume, Source: "demo_pgdata"},
		strings.NewReader("payload"))

	// Assert
	require.ErrorContains(t, err, "clear volume demo_pgdata before restore")
	api.AssertExpectations(t)
}

// WriteVolume's first half on its own, for a backend that writes the data back
// itself: the image, the command and the bind are the wipe policy.
func TestEngine_WipeVolume(t *testing.T) {
	tests := []struct {
		name      string
		src       dockermodel.MountSource
		wantBinds []string
		wantErr   string
	}{
		{
			name:      "a volume is cleared by its docker name",
			src:       dockermodel.MountSource{Type: dockermodel.MountSourceVolume, Source: "demo_pgdata"},
			wantBinds: []string{"demo_pgdata:" + wipeMountPath},
		},
		{
			name:      "a bind is cleared by its host path",
			src:       dockermodel.MountSource{Type: dockermodel.MountSourceBind, Source: "/host/data"},
			wantBinds: []string{"/host/data:" + wipeMountPath},
		},
		{
			name:    "an address with no source never reaches the arcaved",
			src:     dockermodel.MountSource{Type: dockermodel.MountSourceVolume},
			wantErr: "no source",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			var wipe *containerRun
			if tt.wantErr == "" {
				expectImagePresent(api, scratchImage)
				wipe = expectContainerRun(api, "wipe-1", "find")
			}
			e := &Engine{api: api}

			// Act
			err := e.WipeVolume(context.Background(), tt.src)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				api.AssertExpectations(t) // the empty mock is the assertion
				return
			}
			require.NoError(t, err)
			api.AssertExpectations(t)
			assert.Equal(t, scratchImage, wipe.cfg.Image)
			assert.EqualValues(t, []string{"find", wipeMountPath, "-mindepth", "1", "-maxdepth", "1",
				"-exec", "rm", "-rf", "{}", "+"}, wipe.cfg.Cmd)
			assert.Equal(t, tt.wantBinds, wipe.host.Binds)
		})
	}
}

func TestMountAddress(t *testing.T) {
	tests := []struct {
		name    string
		mnt     mountInfo
		want    dockermodel.MountSource
		wantErr string
	}{
		{
			name: "a volume by its docker name",
			mnt:  mountInfo{Type: "volume", Name: "demo_pgdata"},
			want: dockermodel.MountSource{Type: dockermodel.MountSourceVolume, Source: "demo_pgdata"},
		},
		{
			name: "a bind by its host path",
			mnt:  mountInfo{Type: "bind", Source: "/host/cfg"},
			want: dockermodel.MountSource{Type: dockermodel.MountSourceBind, Source: "/host/cfg"},
		},
		{name: "a type no container can bind", mnt: mountInfo{Type: "tmpfs"}, wantErr: "unsupported mount type"},
		{name: "a volume with no name", mnt: mountInfo{Type: "volume"}, wantErr: "cannot resolve backing storage"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := mountSourceOf(tt.mnt)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Equal(t, dockermodel.MountSource{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// namedVolume and anonVolume are the two arms these tests address a volume by.
func namedVolume(dockerName string) dockermodel.Volume {
	return dockermodel.Volume{
		ObjectID:       "volume-named-" + dockerName,
		ComposeProject: "demo",
		Type:           dockermodel.VolumeTypeNamed,
		Named:          &dockermodel.VolumePropertiesNamed{VolumeName: dockerName},
	}
}

func anonVolume(service, target string) dockermodel.Volume {
	return dockermodel.Volume{
		ObjectID:       "volume-service-" + service + target,
		ComposeProject: "demo",
		Type:           dockermodel.VolumeTypeAnon,
		Anon:           &dockermodel.VolumePropertiesAnon{Service: service, Target: target},
	}
}

// containerRun is one throwaway container's expectations, plus what the run
// actually created it with — so an assertion reads the arguments the double was
// handed rather than the mock's journal.
type containerRun struct {
	create, remove *mock.Call
	cfg            *container.Config
	host           *container.HostConfig
	status         chan container.WaitResponse
}

// exit re-answers the container's wait with a non-zero status.
func (r *containerRun) exit(code int64) *containerRun {
	<-r.status
	r.status <- container.WaitResponse{StatusCode: code}
	return r
}

// expectContainerRun drives one throwaway container — told apart from the others
// of the same run by the command it invokes — through to a zero exit under id.
func expectContainerRun(api *mockAPI, id, verb string) *containerRun {
	run := &containerRun{status: make(chan container.WaitResponse, 1)}
	run.status <- container.WaitResponse{}

	run.create = api.On("ContainerCreate", mock.Anything, matchCommand(verb),
		mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(container.CreateResponse{ID: id}, nil).Once().
		Run(func(args mock.Arguments) {
			run.cfg = args.Get(1).(*container.Config)
			run.host = args.Get(2).(*container.HostConfig)
		})
	api.On("ContainerStart", mock.Anything, id, container.StartOptions{}).Return(nil).Once()
	api.On("ContainerWait", mock.Anything, id, container.WaitConditionNotRunning).
		Return((<-chan container.WaitResponse)(run.status), (<-chan error)(make(chan error))).Once()
	run.remove = api.On("ContainerRemove", mock.Anything, id, container.RemoveOptions{Force: true}).
		Return(nil).Once()
	return run
}

// matchCommand matches a container by the command it runs, which is what tells
// the wipe container and the scratch container of one restore apart.
func matchCommand(verb string) any {
	return mock.MatchedBy(func(cfg *container.Config) bool {
		return len(cfg.Cmd) > 0 && cfg.Cmd[0] == verb
	})
}

// matchComposeLabels matches the container listing by the label filter it
// carries: finding a service's container by compose's own labels is the whole
// contract of that call, so it is asserted rather than widened away.
func matchComposeLabels(project, service string) any {
	return mock.MatchedBy(func(opts container.ListOptions) bool {
		want := []string{composeProjectLabel + "=" + project, composeServiceLabel + "=" + service}
		got := opts.Filters.Get("label")
		if !opts.All || len(got) != len(want) {
			return false
		}
		for _, w := range want {
			if !slices.Contains(got, w) {
				return false
			}
		}
		return true
	})
}

// assertNotYet and assertClosed read a moment the mock records: whether the
// removal has already happened at the point the test is standing at.
func assertNotYet(t *testing.T, done <-chan struct{}, msg string) {
	t.Helper()

	select {
	case <-done:
		t.Fatal(msg)
	default:
	}
}

func assertClosed(t *testing.T, done <-chan struct{}, msg string) {
	t.Helper()

	select {
	case <-done:
	default:
		t.Fatal(msg)
	}
}
