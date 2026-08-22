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

var errCompose = errors.New("daemon refused")

// testOptions is the resolved project config a Client is normally built with; its values
// differ from the package fallbacks so a case can tell the two apart.
var testOptions = Options{WaitTimeout: 45 * time.Second, StopTimeout: 25 * time.Second}

func TestClient_Stop(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		stopErr error
		wantErr error
		verify  func(t *testing.T, composeAPI *mockComposeAPI)
	}{
		{
			name: "stops the project it is given with the configured grace period",
			opts: testOptions,
			verify: func(t *testing.T, composeAPI *mockComposeAPI) {
				opts := composeAPI.stopCall(t)
				require.NotNil(t, opts.Project, "compose needs the project to order the shutdown")
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
			name:    "a compose failure reaches the caller",
			stopErr: errCompose,
			wantErr: errCompose,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			composeAPI := &mockComposeAPI{}
			composeAPI.On("Stop", mock.Anything, "demo", mock.Anything).Return(tt.stopErr).Maybe()
			c := NewClient(composeAPI, tt.opts)

			// Act
			err := c.Stop(context.Background(), fixtureProject())

			// Assert
			assertError(t, err, tt.wantErr, "")
			if tt.verify != nil {
				tt.verify(t, composeAPI)
			}
		})
	}
}

// The teardown's options are pinned: a wrong value compiles clean and destroys
// data no savepoint holds.
func TestClient_Down(t *testing.T) {
	// Arrange
	composeAPI := &mockComposeAPI{}
	composeAPI.On("Down", mock.Anything, "demo", mock.Anything).Return(nil).Once()
	c := NewClient(composeAPI, testOptions)

	// Act
	err := c.Down(context.Background(), fixtureProject())

	// Assert
	require.NoError(t, err)
	opts := composeAPI.downCall(t)
	require.NotNil(t, opts.Project)
	assert.False(t, opts.Volumes, "the teardown must not remove volumes")
	assert.True(t, opts.RemoveOrphans)
	assert.Empty(t, opts.Images, "the teardown must not remove images")
	require.NotNil(t, opts.Timeout)
	assert.Equal(t, 25*time.Second, *opts.Timeout)
	composeAPI.AssertExpectations(t)
}

// Create must not inherit and must not recreate: reusing the anonymous volume
// Down orphaned would restore into the volume the rollback is discarding.
func TestClient_Create(t *testing.T) {
	// Arrange
	project := fixtureProject()
	composeAPI := &mockComposeAPI{}
	composeAPI.On("Create", mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
	c := NewClient(composeAPI, testOptions)

	// Act
	err := c.Create(context.Background(), project)

	// Assert
	require.NoError(t, err)
	opts := composeAPI.createCall(t)
	assert.Equal(t, api.RecreateNever, opts.Recreate)
	assert.Equal(t, api.RecreateNever, opts.RecreateDependencies)
	assert.False(t, opts.Inherit)
	assert.True(t, opts.RemoveOrphans)
	composeAPI.AssertExpectations(t)
}

// The compose library leaves its own labels to its CLI; without them the
// containers carry no project and nothing finds the stack again.
func TestClient_Create_StampsComposeLabels(t *testing.T) {
	// Arrange
	project := fixtureProject()
	composeAPI := &mockComposeAPI{}
	composeAPI.On("Create", mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
	c := NewClient(composeAPI, testOptions)

	// Act
	require.NoError(t, c.Create(context.Background(), project))

	// Assert
	stamped := composeAPI.createProject(t)
	assert.Equal(t, types.Labels{
		api.ProjectLabel:     "demo",
		api.ServiceLabel:     "db",
		api.VersionLabel:     api.ComposeVersion,
		api.WorkingDirLabel:  "/work",
		api.ConfigFilesLabel: "/work/a.yml,/work/b.yml",
		api.OneoffLabel:      "False",
	}, stamped.Services["db"].CustomLabels)
	// The caller's project is left alone: it is the one the workflow still holds.
	assert.Empty(t, project.Services["db"].CustomLabels)
	composeAPI.AssertExpectations(t)
}

func TestClient_Start(t *testing.T) {
	tests := []struct {
		name        string
		opts        Options
		waitTimeout time.Duration
		startErr    error
		wantTimeout time.Duration
		wantErr     error
		verify      func(t *testing.T, composeAPI *mockComposeAPI)
	}{
		{
			name:        "a caller's own bound wins over the configured one",
			opts:        testOptions,
			waitTimeout: 30 * time.Second,
			wantTimeout: 30 * time.Second,
		},
		{
			// An MCP tool call omitting wait_timeout lands here: the project's
			// configured bound applies, not the package fallback.
			name:        "falls back to the configured bound when zero",
			opts:        testOptions,
			waitTimeout: 0,
			wantTimeout: 45 * time.Second,
		},
		{
			name:        "falls back to the package default when neither is set",
			waitTimeout: 0,
			wantTimeout: defaultStartWaitTimeout,
		},
		{
			name:        "falls back when negative",
			waitTimeout: -1,
			wantTimeout: defaultStartWaitTimeout,
		},
		{
			name:     "a compose failure reaches the caller",
			startErr: errCompose,
			wantErr:  errCompose,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			composeAPI := &mockComposeAPI{}
			composeAPI.On("Start", mock.Anything, "demo", mock.Anything).Return(tt.startErr).Maybe()
			c := NewClient(composeAPI, tt.opts)

			// Act
			err := c.Start(context.Background(), fixtureProject(), tt.waitTimeout)

			// Assert
			assertError(t, err, tt.wantErr, "")
			if tt.verify != nil {
				tt.verify(t, composeAPI)
			}
			if tt.wantErr == nil {
				assertStartOptions(t, composeAPI, tt.wantTimeout)
			}
		})
	}
}

// assertStartOptions pins what every successful Start asks compose for: without Wait
// it would return before the services are healthy.
func assertStartOptions(t *testing.T, composeAPI *mockComposeAPI, wantTimeout time.Duration) {
	t.Helper()
	opts := composeAPI.startCall(t)
	require.NotNil(t, opts.Project)
	assert.Equal(t, "demo", opts.Project.Name)
	assert.True(t, opts.Wait)
	assert.Equal(t, wantTimeout, opts.WaitTimeout)
}

// fixtureProject is the two-service project a call is handed.
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

func (m *mockComposeAPI) Down(ctx context.Context, projectName string, options api.DownOptions) error {
	return m.Called(ctx, projectName, options).Error(0)
}

func (m *mockComposeAPI) Create(ctx context.Context, project *types.Project, options api.CreateOptions) error {
	return m.Called(ctx, project, options).Error(0)
}

func (m *mockComposeAPI) Start(ctx context.Context, projectName string, options api.StartOptions) error {
	return m.Called(ctx, projectName, options).Error(0)
}

// stopCall returns the options of the one recorded Stop call.
func (m *mockComposeAPI) stopCall(t *testing.T) api.StopOptions {
	t.Helper()
	call := m.calledOnce(t, "Stop")
	return call.Arguments.Get(2).(api.StopOptions)
}

func (m *mockComposeAPI) downCall(t *testing.T) api.DownOptions {
	t.Helper()
	call := m.calledOnce(t, "Down")
	return call.Arguments.Get(2).(api.DownOptions)
}

func (m *mockComposeAPI) createCall(t *testing.T) api.CreateOptions {
	t.Helper()
	call := m.calledOnce(t, "Create")
	return call.Arguments.Get(2).(api.CreateOptions)
}

func (m *mockComposeAPI) createProject(t *testing.T) *types.Project {
	t.Helper()
	call := m.calledOnce(t, "Create")
	return call.Arguments.Get(1).(*types.Project)
}

// assertError is the shared shape of every case's outcome check: a nil wantErr
// means the call had to succeed, otherwise the cause must survive the wrapping.
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

// startCall returns the options of the one recorded Start call.
func (m *mockComposeAPI) startCall(t *testing.T) api.StartOptions {
	t.Helper()
	call := m.calledOnce(t, "Start")
	return call.Arguments.Get(2).(api.StartOptions)
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
