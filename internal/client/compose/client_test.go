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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var errCompose = errors.New("daemon refused")

// testOptions is the resolved project config a Client is normally built with; its values
// differ from the package fallbacks so a case can tell the two apart.
var testOptions = Options{WaitTimeout: 45 * time.Second, StopTimeout: 25 * time.Second}

// stopTimeoutCases are the bounds Stop and Down hand compose: the configured grace
// period is a floor a declared stop_grace_period raises.
var stopTimeoutCases = []struct {
	name        string
	opts        Options
	grace       map[string]time.Duration
	wantTimeout time.Duration
}{
	{
		name:        "no declared grace period",
		opts:        Options{StopTimeout: 10 * time.Second},
		wantTimeout: 10 * time.Second,
	},
	{
		name:        "a declared grace period above the floor raises it",
		opts:        Options{StopTimeout: 10 * time.Second},
		grace:       map[string]time.Duration{"db": 60 * time.Second},
		wantTimeout: 60 * time.Second,
	},
	{
		name:        "a declared grace period below the floor is ignored",
		opts:        Options{StopTimeout: 30 * time.Second},
		grace:       map[string]time.Duration{"db": 5 * time.Second},
		wantTimeout: 30 * time.Second,
	},
	{
		name:        "the package default is the floor when unconfigured",
		grace:       map[string]time.Duration{"db": 15 * time.Second},
		wantTimeout: 15 * time.Second,
	},
	{
		name:        "the package default applies when nothing is declared",
		wantTimeout: defaultStopTimeout,
	},
	{
		// compose truncates to whole seconds: 500ms would reach the daemon as 0.
		name:        "a fractional bound is rounded up to whole seconds",
		opts:        Options{StopTimeout: 500 * time.Millisecond},
		grace:       map[string]time.Duration{"db": 1500 * time.Millisecond},
		wantTimeout: 2 * time.Second,
	},
}

func TestClient_Stop(t *testing.T) {
	for _, tt := range stopTimeoutCases {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			project := graceProject(tt.grace)
			composeAPI := &mockComposeAPI{}
			// Without a Timeout compose applies the container's own, and a database
			// SIGKILLed mid-shutdown is what the next savepoint captures.
			stop := composeAPI.On("Stop", mock.Anything, "demo", mock.MatchedBy(func(opts api.StopOptions) bool {
				return opts.Project == project && opts.Timeout != nil && *opts.Timeout == tt.wantTimeout
			})).Return(nil).Once()
			mock.InOrder(
				composeAPI.On("Ps", mock.Anything, "demo", psRunning(project)).Return(nil, nil).Once(),
				stop,
			)
			c := NewClient(composeAPI, tt.opts)

			// Act
			_, err := c.Stop(context.Background(), project)

			// Assert
			require.NoError(t, err)
			composeAPI.AssertExpectations(t)
		})
	}
}

func TestClient_Stop_composeFailure(t *testing.T) {
	// Arrange
	project := fixtureProject()
	composeAPI := &mockComposeAPI{}
	composeAPI.On("Ps", mock.Anything, "demo", psRunning(project)).
		Return([]api.ContainerSummary{{ID: "c1", Name: "demo-db-1", Service: "db", State: "running"}}, nil).Once()
	composeAPI.On("Stop", mock.Anything, "demo", mock.Anything).Return(errCompose).Once()
	c := NewClient(composeAPI, testOptions)

	// Act
	killed, err := c.Stop(context.Background(), project)

	// Assert
	require.ErrorIs(t, err, errCompose)
	assert.Nil(t, killed)
	composeAPI.AssertExpectations(t)
}

func TestClient_Stop_warnsOfAKilledContainer(t *testing.T) {
	running := []api.ContainerSummary{{ID: "c1", Name: "demo-db-1", Service: "db", State: "running"}}
	exited := func(id, name, service string, code int) api.ContainerSummary {
		return api.ContainerSummary{ID: id, Name: name, Service: service, State: "exited", ExitCode: code}
	}

	tests := []struct {
		name       string
		stopSignal string
		before     []api.ContainerSummary
		beforeErr  error
		after      []api.ContainerSummary
		afterErr   error
		wantWarn   []string
		wantKilled []string
	}{
		{
			name:       "a running container that exited 137 was killed",
			before:     running,
			after:      []api.ContainerSummary{exited("c1", "demo-db-1", "db", 137)},
			wantWarn:   []string{"container demo-db-1 did not exit within 25s and was killed"},
			wantKilled: []string{"c1"},
		},
		{
			name:       "a killed container the list still shows running is warned of",
			before:     running,
			after:      []api.ContainerSummary{{ID: "c1", Name: "demo-db-1", Service: "db", State: "running", ExitCode: 137}},
			wantWarn:   []string{"container demo-db-1 did not exit within 25s and was killed"},
			wantKilled: []string{"c1"},
		},
		{
			name:   "a clean exit is not warned of",
			before: running,
			after:  []api.ContainerSummary{exited("c1", "demo-db-1", "db", 0)},
		},
		{
			name:   "a container not running before is not warned of",
			before: running,
			after:  []api.ContainerSummary{exited("c1", "demo-db-1", "db", 0), exited("c2", "demo-app-1", "app", 137)},
		},
		{
			name:       "a service stopped with SIGKILL exits 137 normally",
			stopSignal: "SIGKILL",
			before:     running,
			after:      []api.ContainerSummary{exited("c1", "demo-db-1", "db", 137)},
		},
		{
			name:      "a failed read before the stop warns of nothing",
			beforeErr: errCompose,
		},
		{
			name:     "a failed read after the stop warns of nothing",
			before:   running,
			afterErr: errCompose,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			project := fixtureProject()
			db := project.Services["db"]
			db.StopSignal = tt.stopSignal
			project.Services["db"] = db
			var logs bytes.Buffer
			ctx := zerolog.New(&logs).WithContext(context.Background())
			composeAPI := &mockComposeAPI{}
			calls := []*mock.Call{
				composeAPI.On("Ps", mock.Anything, "demo", psRunning(project)).Return(tt.before, tt.beforeErr).Once(),
				composeAPI.On("Stop", mock.Anything, "demo", mock.Anything).Return(nil).Once(),
			}
			if tt.beforeErr == nil {
				calls = append(calls, composeAPI.On("Ps", mock.Anything, "demo", mock.MatchedBy(func(opts api.PsOptions) bool {
					return opts.All && opts.Project == project
				})).Return(tt.after, tt.afterErr).Once())
			}
			mock.InOrder(calls...)
			c := NewClient(composeAPI, testOptions)

			// Act
			killed, err := c.Stop(ctx, project)

			// Assert
			require.NoError(t, err, "a failed read is secondary and never fails the stop")
			composeAPI.AssertExpectations(t)
			assert.Equal(t, tt.wantKilled, killed)
			var warned []string
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var record struct{ Level, Message string }
				require.NoError(t, json.Unmarshal([]byte(line), &record))
				if record.Level == "warn" && strings.Contains(record.Message, "was killed") {
					warned = append(warned, record.Message)
				}
			}
			require.Len(t, warned, len(tt.wantWarn))
			for i, want := range tt.wantWarn {
				assert.Contains(t, warned[i], want)
			}
		})
	}
}

// The teardown's options are pinned: a wrong value compiles clean and destroys
// data no savepoint holds.
func TestClient_Down(t *testing.T) {
	for _, tt := range stopTimeoutCases {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			project := graceProject(tt.grace)
			composeAPI := &mockComposeAPI{}
			composeAPI.On("Down", mock.Anything, "demo", mock.Anything).Return(nil).Once()
			c := NewClient(composeAPI, tt.opts)

			// Act
			err := c.Down(context.Background(), project)

			// Assert
			require.NoError(t, err)
			opts := composeAPI.downCall(t)
			require.NotNil(t, opts.Project)
			assert.False(t, opts.Volumes, "the teardown must not remove volumes")
			assert.True(t, opts.RemoveOrphans)
			assert.Empty(t, opts.Images, "the teardown must not remove images")
			require.NotNil(t, opts.Timeout)
			assert.Equal(t, tt.wantTimeout, *opts.Timeout)
			composeAPI.AssertExpectations(t)
		})
	}
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

// graceProject is fixtureProject with each service's stop_grace_period from grace.
func graceProject(grace map[string]time.Duration) *types.Project {
	project := fixtureProject()
	for name, period := range grace {
		service, declared := project.Services[name], types.Duration(period)
		service.StopGracePeriod = &declared
		project.Services[name] = service
	}
	return project
}

// psRunning matches the read of the containers running before a stop.
func psRunning(project *types.Project) any {
	return mock.MatchedBy(func(opts api.PsOptions) bool { return !opts.All && opts.Project == project })
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
