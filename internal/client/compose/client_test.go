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
// containers carry no project and nothing finds the compose project again.
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

// A disabled healthcheck leaves the container no health, and compose's start --wait refuses
// it: Start starts without Wait and waits itself, running where no health is reported.
func TestClient_Start_HealthCheckDisabled(t *testing.T) {
	running := func(service string) api.ContainerSummary {
		return api.ContainerSummary{Name: "demo-" + service + "-1", Service: service, State: "running"}
	}
	withHealth := func(c api.ContainerSummary, health string) api.ContainerSummary {
		c.Health = health
		return c
	}
	exited := func(service string, code int) api.ContainerSummary {
		return api.ContainerSummary{Name: "demo-" + service + "-1", Service: service, State: "exited", ExitCode: code}
	}

	tests := []struct {
		name        string
		project     *types.Project
		waitTimeout time.Duration
		startErr    error
		polls       [][]api.ContainerSummary
		psErr       error
		wantErr     error
		wantErrMsg  string
	}{
		{
			name:    "a running replica without health is ready",
			project: disabledHealthProject(&types.HealthCheckConfig{Disable: true}),
			polls:   [][]api.ContainerSummary{{running("web"), withHealth(running("db"), "healthy")}},
		},
		{
			name:    "a test of NONE is a disabled healthcheck too",
			project: disabledHealthProject(&types.HealthCheckConfig{Test: types.HealthCheckTest{"NONE"}}),
			polls:   [][]api.ContainerSummary{{running("web"), withHealth(running("db"), "healthy")}},
		},
		{
			name:    "waits for a replica still created and a health still starting",
			project: disabledHealthProject(&types.HealthCheckConfig{Disable: true}),
			polls: [][]api.ContainerSummary{
				{{Service: "web", State: "created"}, withHealth(running("db"), "starting")},
				{running("web"), withHealth(running("db"), "starting")},
				{running("web"), withHealth(running("db"), "healthy")},
			},
		},
		{
			name:       "a replica that exited on start fails the wait",
			project:    disabledHealthProject(&types.HealthCheckConfig{Disable: true}),
			polls:      [][]api.ContainerSummary{{exited("web", 1), withHealth(running("db"), "healthy")}},
			wantErrMsg: "container demo-web-1 exited (1)",
		},
		{
			name:       "an unhealthy replica fails the wait",
			project:    disabledHealthProject(&types.HealthCheckConfig{Disable: true}),
			polls:      [][]api.ContainerSummary{{running("web"), withHealth(running("db"), "unhealthy")}},
			wantErrMsg: "container demo-db-1 is unhealthy",
		},
		{
			// Start has already waited for it to complete; its exit is not a failure.
			name:    "a one-shot another depends on to complete is not waited to run",
			project: oneShotProject(),
			polls: [][]api.ContainerSummary{
				{exited("migrate", 0), {Service: "web", State: "created"}},
				{exited("migrate", 0), running("web")},
			},
		},
		{
			name:        "gives up once the bound has passed",
			project:     disabledHealthProject(&types.HealthCheckConfig{Disable: true}),
			waitTimeout: time.Millisecond,
			polls:       nil, // every read reports a replica still starting
			wantErrMsg:  "application not healthy after 1ms",
		},
		{
			name:     "a compose failure reaches the caller unwaited",
			project:  disabledHealthProject(&types.HealthCheckConfig{Disable: true}),
			startErr: errCompose,
			wantErr:  errCompose,
		},
		{
			name:    "a failed read reaches the caller",
			project: disabledHealthProject(&types.HealthCheckConfig{Disable: true}),
			psErr:   errCompose,
			wantErr: errCompose,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			composeAPI := &mockComposeAPI{}
			start := composeAPI.On("Start", mock.Anything, "demo", mock.MatchedBy(func(opts api.StartOptions) bool {
				return !opts.Wait && opts.WaitTimeout > 0 && opts.Project == tt.project
			})).Return(tt.startErr).Once()
			psOptions := mock.MatchedBy(func(opts api.PsOptions) bool {
				return opts.All && opts.Project == tt.project
			})
			switch {
			case tt.startErr != nil:
			case tt.psErr != nil:
				composeAPI.On("Ps", mock.Anything, "demo", psOptions).Return(nil, tt.psErr).Once().NotBefore(start)
			case tt.polls == nil:
				composeAPI.On("Ps", mock.Anything, "demo", psOptions).
					Return([]api.ContainerSummary{withHealth(running("db"), "starting"), running("web")}, nil).NotBefore(start)
			default:
				for _, poll := range tt.polls {
					composeAPI.On("Ps", mock.Anything, "demo", psOptions).Return(poll, nil).Once().NotBefore(start)
				}
			}
			c := NewClient(composeAPI, testOptions)
			c.pollInterval = time.Millisecond

			// Act
			err := c.Start(context.Background(), tt.project, tt.waitTimeout)

			// Assert
			if tt.wantErrMsg != "" {
				require.EqualError(t, err, tt.wantErrMsg)
			} else {
				assertError(t, err, tt.wantErr, "")
			}
			composeAPI.AssertExpectations(t)
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

// disabledHealthProject is a web whose healthcheck is check, beside a db that runs its own.
func disabledHealthProject(check *types.HealthCheckConfig) *types.Project {
	return &types.Project{
		Name: "demo",
		Services: types.Services{
			"web": {Name: "web", Image: "busybox", HealthCheck: check},
			"db":  {Name: "db", Image: "postgres:16"},
		},
	}
}

// oneShotProject is a web with its healthcheck disabled, waiting on a migrate to complete.
func oneShotProject() *types.Project {
	return &types.Project{
		Name: "demo",
		Services: types.Services{
			"migrate": {Name: "migrate", Image: "busybox"},
			"web": {
				Name: "web", Image: "busybox", HealthCheck: &types.HealthCheckConfig{Disable: true},
				DependsOn: types.DependsOnConfig{
					"migrate": {Condition: types.ServiceConditionCompletedSuccessfully, Required: true},
				},
			},
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

func (m *mockComposeAPI) Ps(ctx context.Context, projectName string, options api.PsOptions) ([]api.ContainerSummary, error) {
	args := m.Called(ctx, projectName, options)
	containers, _ := args.Get(0).([]api.ContainerSummary)
	return containers, args.Error(1)
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
