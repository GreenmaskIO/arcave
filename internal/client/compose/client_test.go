package compose

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// The two failures a call can meet, as sentinels so a case can assert the cause
// survives the wrapping.
var (
	errLoad    = errors.New("bad compose file")
	errCompose = errors.New("daemon refused")
)

// testOptions is the resolved project config a Client is normally built with;
// its values are distinct from the package fallbacks so a case can tell the two
// apart.
var testOptions = Options{WaitTimeout: 45 * time.Second, StopTimeout: 25 * time.Second}

func TestClient_Stop(t *testing.T) {
	tests := []struct {
		name       string
		opts       Options
		loadErr    error
		stopErr    error
		wantErr    error
		wantErrMsg string
		verify     func(t *testing.T, composeAPI *mockComposeAPI)
	}{
		{
			name: "stops the loaded project with the configured grace period",
			opts: testOptions,
			verify: func(t *testing.T, composeAPI *mockComposeAPI) {
				opts := composeAPI.stopCall(t)
				require.NotNil(t, opts.Project, "compose needs the project to know which services to stop")
				assertLabelled(t, opts.Project)
				// Without a Timeout compose applies the container's own, and a
				// database SIGKILLed mid-shutdown is what the next savepoint captures.
				require.NotNil(t, opts.Timeout, "a stop with no bound is a stop arcave does not control")
				assert.Equal(t, 25*time.Second, *opts.Timeout)
			},
		},
		{
			name: "falls back to the package default when unconfigured",
			verify: func(t *testing.T, composeAPI *mockComposeAPI) {
				opts := composeAPI.stopCall(t)
				require.NotNil(t, opts.Timeout)
				assert.Equal(t, defaultStopTimeout, *opts.Timeout)
			},
		},
		{
			name:       "a loader failure aborts before compose is touched",
			loadErr:    errLoad,
			wantErr:    errLoad,
			wantErrMsg: "loading docker compose file(s)",
			verify: func(t *testing.T, composeAPI *mockComposeAPI) {
				composeAPI.AssertNotCalled(t, "Stop", mock.Anything, mock.Anything, mock.Anything)
			},
		},
		{
			name:    "a compose failure reaches the caller",
			stopErr: errCompose,
			wantErr: errCompose,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			loader := &mockLoader{}
			loader.On("Load", mock.Anything, []string{"a.yml", "b.yml"}).Return(loadResult(tt.loadErr)).Once()
			composeAPI := &mockComposeAPI{}
			composeAPI.On("Stop", mock.Anything, "demo", mock.Anything).Return(tt.stopErr).Maybe()
			c := NewClient(composeAPI, loader, tt.opts)

			// Act
			err := c.Stop(context.Background(), "a.yml", "b.yml")

			// Assert
			assertError(t, err, tt.wantErr, tt.wantErrMsg)
			loader.AssertExpectations(t)
			if tt.verify != nil {
				tt.verify(t, composeAPI)
			}
		})
	}
}

func TestClient_Up(t *testing.T) {
	tests := []struct {
		name        string
		opts        Options
		waitTimeout time.Duration
		loadErr     error
		upErr       error
		wantTimeout time.Duration
		wantStop    time.Duration
		wantErr     error
		wantErrMsg  string
		verify      func(t *testing.T, composeAPI *mockComposeAPI)
	}{
		{
			name:        "a caller's own bound wins over the configured one",
			opts:        testOptions,
			waitTimeout: 30 * time.Second,
			wantTimeout: 30 * time.Second,
			wantStop:    25 * time.Second,
		},
		{
			// An MCP tool call omitting wait_timeout lands here: the project's
			// configured bound applies, not the package fallback.
			name:        "falls back to the configured bound when zero",
			opts:        testOptions,
			waitTimeout: 0,
			wantTimeout: 45 * time.Second,
			wantStop:    25 * time.Second,
		},
		{
			name:        "falls back to the package default when neither is set",
			waitTimeout: 0,
			wantTimeout: defaultUpWaitTimeout,
			wantStop:    defaultStopTimeout,
		},
		{
			name:        "falls back when negative",
			waitTimeout: -1,
			wantTimeout: defaultUpWaitTimeout,
			wantStop:    defaultStopTimeout,
		},
		{
			name:       "a loader failure aborts before compose is touched",
			loadErr:    errLoad,
			wantErr:    errLoad,
			wantErrMsg: "loading docker compose file(s)",
			verify: func(t *testing.T, composeAPI *mockComposeAPI) {
				composeAPI.AssertNotCalled(t, "Up", mock.Anything, mock.Anything, mock.Anything)
			},
		},
		{
			name:    "a compose failure reaches the caller",
			upErr:   errCompose,
			wantErr: errCompose,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			loader := &mockLoader{}
			loader.On("Load", mock.Anything, []string{"a.yml"}).Return(loadResult(tt.loadErr)).Once()
			composeAPI := &mockComposeAPI{}
			composeAPI.On("Up", mock.Anything, mock.Anything, mock.Anything).Return(tt.upErr).Maybe()
			c := NewClient(composeAPI, loader, tt.opts)

			// Act
			err := c.Up(context.Background(), tt.waitTimeout, "a.yml")

			// Assert
			assertError(t, err, tt.wantErr, tt.wantErrMsg)
			loader.AssertExpectations(t)
			if tt.verify != nil {
				tt.verify(t, composeAPI)
			}
			if tt.wantErr == nil {
				assertUpOptions(t, composeAPI, tt.wantTimeout, tt.wantStop)
			}
		})
	}
}

// assertUpOptions pins what every successful Up must ask compose for, whatever
// bound the caller named.
func assertUpOptions(t *testing.T, composeAPI *mockComposeAPI, wantTimeout, wantStop time.Duration) {
	t.Helper()
	project, opts := composeAPI.upCall(t)
	assert.Equal(t, "demo", project.Name)
	assertLabelled(t, project)

	assert.True(t, opts.Start.Wait, "without Wait, Up would return before the services are healthy")
	assert.Equal(t, wantTimeout, opts.Start.WaitTimeout)

	// Recreating a container stops the one it replaces, so the grace period must
	// reach Up as well — a service arcave shut down cleanly for the snapshot would
	// otherwise be SIGKILLed on the way back up.
	require.NotNil(t, opts.Create.Timeout, "a recreate with no bound is a stop arcave does not control")
	assert.Equal(t, wantStop, *opts.Create.Timeout)
	assert.Same(t, project, opts.Start.Project)
	assert.ElementsMatch(t, []string{"app", "db"}, opts.Start.Services)
	assert.ElementsMatch(t, []string{"app", "db"}, opts.Create.Services)

	// The library's zero CreateOptions means "renew anonymous volumes", the
	// opposite of the CLI's default: a container recreated because the compose
	// file changed would lose data arcave promises to keep.
	assert.True(t, opts.Create.Inherit, "a recreated container must inherit its anonymous volumes")
	assert.Equal(t, api.RecreateDiverged, opts.Create.Recreate)
	assert.Equal(t, api.RecreateDiverged, opts.Create.RecreateDependencies)
}

// Compose does not label the project for you when it is called as a library, and
// without the labels it does not recognise its own containers: an Up would
// recreate a stack it should have reused, and a Stop would find nothing to stop.
func TestLabelProject(t *testing.T) {
	// Arrange
	project := fixtureProject()

	// Act
	got := labelProject(project)

	// Assert
	require.Len(t, got.Services, 2)
	for name, service := range got.Services {
		assert.Equal(t, types.Labels{
			api.ProjectLabel:     "demo",
			api.ServiceLabel:     name,
			api.VersionLabel:     api.ComposeVersion,
			api.WorkingDirLabel:  "/work",
			api.ConfigFilesLabel: "/work/a.yml,/work/b.yml",
			api.OneoffLabel:      "False",
		}, service.CustomLabels)
	}
}

// assertError is the shared shape of every case's outcome check: a nil wantErr
// means the call had to succeed, otherwise the cause must survive whatever
// context Client wrapped around it.
func assertError(t *testing.T, err, wantErr error, wantErrMsg string) {
	t.Helper()
	if wantErr == nil {
		require.NoError(t, err)
		return
	}
	require.ErrorIs(t, err, wantErr)
	if wantErrMsg != "" {
		assert.Contains(t, err.Error(), wantErrMsg)
	}
}

// loadResult is what the loader double returns for a case: a fresh project, or
// nothing at all when the case makes the load fail. It returns both values so a
// case can spell the expectation as a single Return.
func loadResult(loadErr error) (*types.Project, error) {
	if loadErr != nil {
		return nil, loadErr
	}
	return fixtureProject(), nil
}

func assertLabelled(t *testing.T, project *types.Project) {
	t.Helper()
	for name, service := range project.Services {
		assert.Equal(t, project.Name, service.CustomLabels[api.ProjectLabel])
		assert.Equal(t, name, service.CustomLabels[api.ServiceLabel])
		assert.Equal(t, api.ComposeVersion, service.CustomLabels[api.VersionLabel])
		assert.Equal(t, "False", service.CustomLabels[api.OneoffLabel])
	}
}

// fixtureProject is what a loader hands back: an unlabelled two-service project.
func fixtureProject() *types.Project {
	return &types.Project{
		Name:         "demo",
		WorkingDir:   "/work",
		ComposeFiles: []string{"/work/a.yml", "/work/b.yml"},
		Services: types.Services{
			"db":  {Image: "postgres:16"},
			"app": {Image: "busybox"},
		},
	}
}

// mockComposeAPI is the dockerComposeAPI double: it records what arcave asks compose
// to do, so the options are asserted without a daemon.
type mockComposeAPI struct {
	mock.Mock
}

func (m *mockComposeAPI) Stop(ctx context.Context, projectName string, options api.StopOptions) error {
	return m.Called(ctx, projectName, options).Error(0)
}

func (m *mockComposeAPI) Up(ctx context.Context, project *types.Project, options api.UpOptions) error {
	return m.Called(ctx, project, options).Error(0)
}

// stopCall returns the options of the one recorded Stop call.
func (m *mockComposeAPI) stopCall(t *testing.T) api.StopOptions {
	t.Helper()
	call := m.calledOnce(t, "Stop")
	return call.Arguments.Get(2).(api.StopOptions)
}

// upCall returns the project and options of the one recorded Up call.
func (m *mockComposeAPI) upCall(t *testing.T) (*types.Project, api.UpOptions) {
	t.Helper()
	call := m.calledOnce(t, "Up")
	return call.Arguments.Get(1).(*types.Project), call.Arguments.Get(2).(api.UpOptions)
}

func (m *mockComposeAPI) calledOnce(t *testing.T, method string) mock.Call {
	t.Helper()
	var found []mock.Call
	for _, call := range m.Calls {
		if call.Method == method {
			found = append(found, call)
		}
	}
	require.Len(t, found, 1, "expected exactly one %s call", method)
	return found[0]
}

// mockLoader is the dockerComposeLoader double. It takes the variadic files as a
// single []string so an expectation can name them.
type mockLoader struct {
	mock.Mock
}

func (m *mockLoader) Load(ctx context.Context, files ...string) (*types.Project, error) {
	args := m.Called(ctx, files)
	project, _ := args.Get(0).(*types.Project)
	return project, args.Error(1)
}
