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

package hold

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// newTestHook is a hook running command, failing the workflow or not by onError.
func newTestHook(command string, onError model.HookOnError) *composemodel.Hook {
	return &composemodel.Hook{
		Command: []string{command},
		Timeout: time.Minute,
		OnError: onError,
	}
}

// expectExec expects command run once in the container id.
func expectExec(control *composetest.MockContainerControl, id, command string) *mock.Call {
	return control.On("ExecInContainer", mock.Anything, id, composemodel.ExecSpec{Cmd: []string{command}}).Once()
}

// exitedWith is an exec that exited with code, stderr its output.
func exitedWith(code int, stderr string) composemodel.ExecResult {
	return composemodel.ExecResult{
		ExitCode: code,
		Stderr:   []byte(stderr),
	}
}

func TestNewHookTrigger(t *testing.T) {
	var (
		pre        = newTestHook("pre", model.HookOnErrorFail)
		post       = newTestHook("post", model.HookOnErrorFail)
		pausedWeb2 = composemodel.IdleReplica{Ref: webReplica2, State: "paused"}
	)

	tests := []struct {
		name     string
		services []composemodel.ServiceHold
		want     hookTrigger
	}{
		{
			name: "a service with no hook runs none",
			services: []composemodel.ServiceHold{
				{Service: "db", Running: []composemodel.ContainerRef{dbReplica1}},
			},
			want: hookTrigger{},
		},
		{
			name: "a pre-capture hook in the running replicas, the idle ones skipped",
			services: []composemodel.ServiceHold{
				{Service: "web", Running: []composemodel.ContainerRef{webReplica1}, Idle: []composemodel.IdleReplica{pausedWeb2},
					PreCapture: pre},
			},
			want: hookTrigger{
				pre:        []hookTarget{{ref: webReplica1, hook: *pre}},
				skippedPre: []composemodel.IdleReplica{pausedWeb2},
			},
		},
		{
			name: "a post-capture hook in every replica, by replica",
			services: []composemodel.ServiceHold{
				{Service: "web", Running: []composemodel.ContainerRef{webReplica2}, Idle: []composemodel.IdleReplica{
					{Ref: webReplica1, State: "exited"},
				}, PostCapture: post},
			},
			want: hookTrigger{
				post: []hookTarget{{ref: webReplica1, hook: *post}, {ref: webReplica2, hook: *post}},
			},
		},
		{
			name: "every service with its own, in the plan's order",
			services: []composemodel.ServiceHold{
				{Service: "db", Running: []composemodel.ContainerRef{dbReplica1}, PreCapture: pre, PostCapture: post},
				{Service: "worker", Running: []composemodel.ContainerRef{workerReplica1}, PostCapture: post},
			},
			want: hookTrigger{
				pre:  []hookTarget{{ref: dbReplica1, hook: *pre}},
				post: []hookTarget{{ref: dbReplica1, hook: *post}, {ref: workerReplica1, hook: *post}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			control := &composetest.MockContainerControl{}
			tt.want.control = control

			// Act
			got := newHookTrigger(control, tt.services)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// The hooks are the first trigger: the pre-capture ones run before anything is held, the
// post-capture ones once everything is back.
func TestNewTriggersByMode_HooksFirst(t *testing.T) {
	// Arrange
	runner := &composetest.MockComposeRunner{}
	control := &composetest.MockContainerControl{}
	pre := newTestHook("pre", model.HookOnErrorFail)
	plan := composemodel.Plan{Project: demoSpec, Services: []composemodel.ServiceHold{
		{Service: "db", Mode: model.CaptureModeStop, Running: []composemodel.ContainerRef{dbReplica1}, PreCapture: pre},
	}}

	// Act
	triggers := NewTriggersByMode(runner, control, plan, time.Minute)

	// Assert
	require.Len(t, triggers, 4)
	assert.Equal(t, hookTrigger{control: control, pre: []hookTarget{{ref: dbReplica1, hook: *pre}}}, triggers[0])
}

func TestRunHooks(t *testing.T) {
	var (
		fail       = *newTestHook("hook", model.HookOnErrorFail)
		continuing = *newTestHook("hook", model.HookOnErrorContinue)
		errBoom    = errors.New("boom")
	)

	tests := []struct {
		name    string
		phase   model.HookPhase
		targets []hookTarget
		// results answer each container's exec, by its id.
		results map[string]composemodel.ExecResult
		errs    map[string]error
		want    []model.FailedHook
		wantErr string
		// wantWarnings are the warnings given, and whether each is marked InResult.
		wantWarnings []string
		wantInResult []bool
	}{
		{
			name:    "every hook passes",
			phase:   model.HookPhasePreCapture,
			targets: []hookTarget{{ref: dbReplica1, hook: fail}, {ref: webReplica1, hook: fail}},
		},
		{
			name:    "a failed hook fails, the others run to their end, its last line of stderr quoted",
			phase:   model.HookPhasePreCapture,
			targets: []hookTarget{{ref: dbReplica1, hook: fail}, {ref: webReplica1, hook: fail}},
			results: map[string]composemodel.ExecResult{"db1": exitedWith(3, "starting\npsql: connection refused\n\n")},
			wantErr: "pre-capture hook of db-1 failed: exit 3: psql: connection refused",
		},
		{
			name:    "a failed hook with a blank stderr quotes its stdout, where Docker says why it did not start",
			phase:   model.HookPhasePreCapture,
			targets: []hookTarget{{ref: dbReplica1, hook: fail}},
			results: map[string]composemodel.ExecResult{"db1": {
				ExitCode: 127,
				Stdout:   []byte("OCI runtime exec failed: exec: \"psql\": executable file not found in $PATH\r\n"),
				Stderr:   []byte("\n"),
			}},
			wantErr: "pre-capture hook of db-1 failed: exit 127: " +
				"OCI runtime exec failed: exec: \"psql\": executable file not found in $PATH",
		},
		{
			name:    "of two failed, the first by the targets is returned, naming the other",
			phase:   model.HookPhasePostCapture,
			targets: []hookTarget{{ref: dbReplica1, hook: fail}, {ref: webReplica1, hook: fail}},
			results: map[string]composemodel.ExecResult{"db1": exitedWith(1, ""), "web1": exitedWith(2, "")},
			wantErr: "post-capture hook of db-1 failed: exit 1; and 1 more hook failed",
		},
		{
			name:    "a pre-capture failure continue lets through is a warning the result repeats, and recorded",
			phase:   model.HookPhasePreCapture,
			targets: []hookTarget{{ref: dbReplica1, hook: continuing}},
			results: map[string]composemodel.ExecResult{"db1": exitedWith(3, "boom")},
			want: []model.FailedHook{{
				Service:   "db",
				Replica:   1,
				Container: "demo-db-1",
				Hook:      model.HookPhasePreCapture,
				ExitCode:  new(3),
				Error:     "exit 3: boom",
			}},
			wantWarnings: []string{"pre-capture hook of db-1 failed: exit 3: boom; continuing"},
			wantInResult: []bool{true},
		},
		{
			name:    "a post-capture failure continue lets through is a warning the result does not repeat",
			phase:   model.HookPhasePostCapture,
			targets: []hookTarget{{ref: dbReplica1, hook: continuing}},
			errs:    map[string]error{"db1": errBoom},
			want: []model.FailedHook{{
				Service:   "db",
				Replica:   1,
				Container: "demo-db-1",
				Hook:      model.HookPhasePostCapture,
				Error:     "boom",
			}},
			wantWarnings: []string{"post-capture hook of db-1 failed: boom; continuing"},
			wantInResult: []bool{false},
		},
		{
			name:         "a replica not running is skipped with a warning, whatever on-error says",
			phase:        model.HookPhasePostCapture,
			targets:      []hookTarget{{ref: dbReplica1, hook: fail}},
			errs:         map[string]error{"db1": composemodel.ErrContainerNotRunning},
			wantWarnings: []string{"db-1 is not running: post-capture hook skipped"},
			wantInResult: []bool{false},
		},
		{
			name:         "a replica not running is skipped under continue too, and recorded nowhere",
			phase:        model.HookPhasePreCapture,
			targets:      []hookTarget{{ref: dbReplica1, hook: continuing}},
			errs:         map[string]error{"db1": composemodel.ErrContainerNotRunning},
			wantWarnings: []string{"db-1 is not running: pre-capture hook skipped"},
			wantInResult: []bool{false},
		},
		{
			name:    "the daemon's failure is the hook's",
			phase:   model.HookPhasePreCapture,
			targets: []hookTarget{{ref: dbReplica1, hook: fail}},
			errs:    map[string]error{"db1": errBoom},
			wantErr: "pre-capture hook of db-1 failed: boom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			control := &composetest.MockContainerControl{}
			for _, target := range tt.targets {
				expectExec(control, target.ref.ID, "hook").Return(tt.results[target.ref.ID], tt.errs[target.ref.ID])
			}
			recorder := &composetest.WarnedRecorder{}

			// Act
			got, err := runHooks(composetest.NewProgressContext(recorder), control, tt.phase, tt.targets)

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
				assert.ErrorAs(t, err, new(*hookError))
			}
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantWarnings, recorder.Msgs)
			assert.Equal(t, tt.wantInResult, recorder.InResult)
			control.AssertExpectations(t)
		})
	}
}

// A hook past its own timeout fails by its on-error.
func TestRunHooks_Timeout(t *testing.T) {
	tests := []struct {
		name    string
		onError model.HookOnError
		want    []model.FailedHook
		wantErr string
	}{
		{
			name:    "fail",
			onError: model.HookOnErrorFail,
			wantErr: "pre-capture hook of db-1 failed: timed out after 1ms",
		},
		{
			name:    "continue records it with no exit code",
			onError: model.HookOnErrorContinue,
			want: []model.FailedHook{{
				Service:   "db",
				Replica:   1,
				Container: "demo-db-1",
				Hook:      model.HookPhasePreCapture,
				Error:     "timed out after 1ms",
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			control := &composetest.MockContainerControl{}
			hook := composemodel.Hook{Command: []string{"hook"}, Timeout: time.Millisecond, OnError: tt.onError}
			expectExec(control, "db1", "hook").
				Run(func(args mock.Arguments) { <-args.Get(0).(context.Context).Done() }).
				Return(composemodel.ExecResult{}, context.DeadlineExceeded)

			// Act
			got, err := runHooks(composetest.NewTestContext(), control, model.HookPhasePreCapture,
				[]hookTarget{{ref: dbReplica1, hook: hook}})

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
				assert.ErrorAs(t, err, new(*hookError))
			}
			assert.Equal(t, tt.want, got)
			control.AssertExpectations(t)
		})
	}
}

// An interrupt of the workflow is no hook's failure: it is returned as it is.
func TestRunHooks_Interrupt(t *testing.T) {
	// Arrange
	control := &composetest.MockContainerControl{}
	hook := *newTestHook("hook", model.HookOnErrorContinue)
	ctx, cancel := context.WithCancel(composetest.NewTestContext())
	expectExec(control, "db1", "hook").
		Run(func(mock.Arguments) { cancel() }).
		Return(composemodel.ExecResult{}, context.Canceled)

	// Act
	failed, err := runHooks(ctx, control, model.HookPhasePreCapture, []hookTarget{{ref: dbReplica1, hook: hook}})

	// Assert
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotErrorAs(t, err, new(*hookError))
	assert.Nil(t, failed)
	control.AssertExpectations(t)
}

// An interrupt outranks a hook's failure, whichever target it came on.
func TestRunHooks_InterruptOutranksAFailure(t *testing.T) {
	// Arrange
	control := &composetest.MockContainerControl{}
	hook := *newTestHook("hook", model.HookOnErrorFail)
	ctx, cancel := context.WithCancel(composetest.NewTestContext())
	expectExec(control, "db1", "hook").Return(exitedWith(3, "boom"), nil)
	expectExec(control, "web1", "hook").
		Run(func(mock.Arguments) { cancel() }).
		Return(composemodel.ExecResult{}, context.Canceled)

	// Act
	_, err := runHooks(ctx, control, model.HookPhasePreCapture,
		[]hookTarget{{ref: dbReplica1, hook: hook}, {ref: webReplica1, hook: hook}})

	// Assert
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotErrorAs(t, err, new(*hookError))
	control.AssertExpectations(t)
}

// A batch per phase with a step per replica; no hook, no batch.
func TestHookTrigger_beforeCapture(t *testing.T) {
	hook := *newTestHook("hook", model.HookOnErrorFail)

	tests := []struct {
		name string
		pre  []hookTarget
		want []progress.Kind
	}{
		{
			name: "no hook: no step, not even the root",
		},
		{
			name: "two replicas",
			pre:  []hookTarget{{ref: dbReplica1, hook: hook}, {ref: webReplica1, hook: hook}},
			want: []progress.Kind{"test", kindHookPreCapture, kindHook, kindHook},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			control := &composetest.MockContainerControl{}
			for _, target := range tt.pre {
				expectExec(control, target.ref.ID, "hook").Return(composemodel.ExecResult{}, nil)
			}
			trigger := hookTrigger{control: control, pre: tt.pre}
			recorder := &composetest.StepRecorder{}

			// Act
			outcome, err := trigger.beforeCapture(composetest.NewProgressContext(recorder))

			// Assert
			require.NoError(t, err)
			assert.Zero(t, outcome)
			assert.Equal(t, tt.want, recorder.Kinds)
			control.AssertExpectations(t)
		})
	}
}

// The hooks through every trigger of a plan, as a savepoint holds and releases it: db stopped,
// web paused, each with its hooks.
func TestTriggers(t *testing.T) {
	var (
		fail       = model.HookOnErrorFail
		continuing = model.HookOnErrorContinue
		errBoom    = errors.New("boom")
	)
	type hooks struct {
		pre, post *composemodel.Hook
	}

	tests := []struct {
		name          string
		db, web       hooks
		webIdle       []composemodel.IdleReplica
		expect        func(runner *composetest.MockComposeRunner, control *composetest.MockContainerControl)
		wantBeforeErr string
		wantOutcome   Outcome
		wantAfterErr  string
		wantWarnings  []string
		wantInResult  []bool
	}{
		{
			name: "a failed pre-capture hook holds nothing, and the post-capture hooks run after the start",
			db:   hooks{pre: newTestHook("db-pre", fail), post: newTestHook("db-post", fail)},
			web:  hooks{pre: newTestHook("web-pre", fail)},
			expect: func(runner *composetest.MockComposeRunner, control *composetest.MockContainerControl) {
				expectExec(control, "db1", "db-pre").Return(exitedWith(3, "not ready"), nil)
				expectExec(control, "web1", "web-pre").Return(composemodel.ExecResult{}, nil)
				unpause := control.On("UnpauseContainers", mock.Anything, []string{"web1"}).Return(nil).Once()
				await := control.On("AwaitHealthProbes", mock.Anything, []string{"web1"}).Return(nil).Once()
				start := runner.On("Start", mock.Anything, demoSpec, time.Minute).Return(nil).Once()
				post := expectExec(control, "db1", "db-post").Return(composemodel.ExecResult{}, nil)
				mock.InOrder(unpause, await, start, post)
			},
			wantBeforeErr: "pre-capture hook of db-1 failed: exit 3: not ready",
		},
		{
			name: "a failed stop after the pre-capture hooks captures nothing, and brings everything back",
			db:   hooks{pre: newTestHook("db-pre", fail), post: newTestHook("db-post", fail)},
			expect: func(runner *composetest.MockComposeRunner, control *composetest.MockContainerControl) {
				pre := expectExec(control, "db1", "db-pre").Return(composemodel.ExecResult{}, nil)
				stop := runner.On("StopServices", mock.Anything, demoSpec, []string{"db"}).Return([]string(nil), errBoom).Once()
				unpause := control.On("UnpauseContainers", mock.Anything, []string{"web1"}).Return(nil).Once()
				await := control.On("AwaitHealthProbes", mock.Anything, []string{"web1"}).Return(nil).Once()
				start := runner.On("Start", mock.Anything, demoSpec, time.Minute).Return(nil).Once()
				post := expectExec(control, "db1", "db-post").Return(composemodel.ExecResult{}, nil)
				mock.InOrder(pre, stop, unpause, await, start, post)
			},
			wantBeforeErr: "stopping services: boom",
		},
		{
			name: "a pre-capture failure let through holds on, and is recorded",
			db:   hooks{pre: newTestHook("db-pre", continuing)},
			expect: func(runner *composetest.MockComposeRunner, control *composetest.MockContainerControl) {
				pre := expectExec(control, "db1", "db-pre").Return(exitedWith(3, "boom"), nil)
				stop := runner.On("StopServices", mock.Anything, demoSpec, []string{"db"}).Return([]string(nil), nil).Once()
				pause := control.On("PauseContainers", mock.Anything, []string{"web1"}).Return(nil).Once()
				unpause := control.On("UnpauseContainers", mock.Anything, []string{"web1"}).Return(nil).Once()
				await := control.On("AwaitHealthProbes", mock.Anything, []string{"web1"}).Return(nil).Once()
				start := runner.On("Start", mock.Anything, demoSpec, time.Minute).Return(nil).Once()
				mock.InOrder(pre, stop, pause, unpause, await, start)
			},
			wantOutcome: Outcome{FailedHooks: []model.FailedHook{{
				Service:   "db",
				Replica:   1,
				Container: "demo-db-1",
				Hook:      model.HookPhasePreCapture,
				ExitCode:  new(3),
				Error:     "exit 3: boom",
			}}},
			wantWarnings: []string{"pre-capture hook of db-1 failed: exit 3: boom; continuing"},
			wantInResult: []bool{true},
		},
		{
			name: "a post-capture failure let through is only a warning",
			web:  hooks{post: newTestHook("web-post", continuing)},
			expect: func(runner *composetest.MockComposeRunner, control *composetest.MockContainerControl) {
				stop := runner.On("StopServices", mock.Anything, demoSpec, []string{"db"}).Return([]string(nil), nil).Once()
				pause := control.On("PauseContainers", mock.Anything, []string{"web1"}).Return(nil).Once()
				unpause := control.On("UnpauseContainers", mock.Anything, []string{"web1"}).Return(nil).Once()
				await := control.On("AwaitHealthProbes", mock.Anything, []string{"web1"}).Return(nil).Once()
				start := runner.On("Start", mock.Anything, demoSpec, time.Minute).Return(nil).Once()
				post := expectExec(control, "web1", "web-post").Return(exitedWith(1, "lock held"), nil)
				mock.InOrder(stop, pause, unpause, await, start, post)
			},
			wantWarnings: []string{"post-capture hook of web-1 failed: exit 1: lock held; continuing"},
			wantInResult: []bool{false},
		},
		{
			name:    "a replica frozen on entry skips its pre-capture hook, and runs its post-capture one once resumed",
			web:     hooks{pre: newTestHook("web-pre", fail), post: newTestHook("web-post", fail)},
			webIdle: []composemodel.IdleReplica{{Ref: webReplica2, State: "paused"}},
			expect: func(runner *composetest.MockComposeRunner, control *composetest.MockContainerControl) {
				pre := expectExec(control, "web1", "web-pre").Return(composemodel.ExecResult{}, nil)
				stop := runner.On("StopServices", mock.Anything, demoSpec, []string{"db"}).Return([]string(nil), nil).Once()
				pause := control.On("PauseContainers", mock.Anything, []string{"web1"}).Return(nil).Once()
				unpause := control.On("UnpauseContainers", mock.Anything, []string{"web1", "web2"}).Return(nil).Once()
				await := control.On("AwaitHealthProbes", mock.Anything, []string{"web1", "web2"}).Return(nil).Once()
				start := runner.On("Start", mock.Anything, demoSpec, time.Minute).Return(nil).Once()
				expectExec(control, "web1", "web-post").Return(composemodel.ExecResult{}, nil).NotBefore(start)
				expectExec(control, "web2", "web-post").Return(composemodel.ExecResult{}, nil).NotBefore(start)
				mock.InOrder(pre, stop, pause, unpause, await, start)
			},
			wantWarnings: []string{
				"web-2 was paused: pre-capture hook skipped",
				"web-2 was paused before the capture: resuming it",
			},
			wantInResult: []bool{false, false},
		},
		{
			name: "a failed post-capture hook fails the release, every other run",
			db:   hooks{post: newTestHook("db-post", fail)},
			web:  hooks{post: newTestHook("web-post", fail)},
			expect: func(runner *composetest.MockComposeRunner, control *composetest.MockContainerControl) {
				runner.On("StopServices", mock.Anything, demoSpec, []string{"db"}).Return([]string(nil), nil).Once()
				control.On("PauseContainers", mock.Anything, []string{"web1"}).Return(nil).Once()
				control.On("UnpauseContainers", mock.Anything, []string{"web1"}).Return(nil).Once()
				control.On("AwaitHealthProbes", mock.Anything, []string{"web1"}).Return(nil).Once()
				start := runner.On("Start", mock.Anything, demoSpec, time.Minute).Return(nil).Once()
				expectExec(control, "db1", "db-post").Return(composemodel.ExecResult{}, nil).NotBefore(start)
				expectExec(control, "web1", "web-post").Return(exitedWith(1, "lock held"), nil).NotBefore(start)
			},
			wantAfterErr: "post-capture hook of web-1 failed: exit 1: lock held",
		},
		{
			name: "a failed start runs the post-capture hooks all the same, a replica not up skipped",
			db:   hooks{post: newTestHook("db-post", fail)},
			web:  hooks{post: newTestHook("web-post", fail)},
			expect: func(runner *composetest.MockComposeRunner, control *composetest.MockContainerControl) {
				runner.On("StopServices", mock.Anything, demoSpec, []string{"db"}).Return([]string(nil), nil).Once()
				control.On("PauseContainers", mock.Anything, []string{"web1"}).Return(nil).Once()
				control.On("UnpauseContainers", mock.Anything, []string{"web1"}).Return(nil).Once()
				control.On("AwaitHealthProbes", mock.Anything, []string{"web1"}).Return(nil).Once()
				start := runner.On("Start", mock.Anything, demoSpec, time.Minute).Return(errBoom).Once()
				expectExec(control, "db1", "db-post").Return(composemodel.ExecResult{}, composemodel.ErrContainerNotRunning).NotBefore(start)
				expectExec(control, "web1", "web-post").Return(composemodel.ExecResult{}, nil).NotBefore(start)
			},
			wantAfterErr: "starting services: boom",
			wantWarnings: []string{"db-1 is not running: post-capture hook skipped"},
			wantInResult: []bool{false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			runner := &composetest.MockComposeRunner{}
			control := &composetest.MockContainerControl{}
			tt.expect(runner, control)
			plan := composemodel.Plan{Project: demoSpec, Services: []composemodel.ServiceHold{
				{Service: "db", Mode: model.CaptureModeStop, Running: []composemodel.ContainerRef{dbReplica1},
					PreCapture: tt.db.pre, PostCapture: tt.db.post},
				{Service: "web", Mode: model.CaptureModePause, Running: []composemodel.ContainerRef{webReplica1}, Idle: tt.webIdle,
					PreCapture: tt.web.pre, PostCapture: tt.web.post},
			}}
			triggers := NewTriggersByMode(runner, control, plan, time.Minute)
			recorder := &composetest.WarnedRecorder{}
			ctx := composetest.NewProgressContext(recorder)

			// Act
			outcome, beforeErr := triggers.BeforeCapture(ctx)
			afterErr := triggers.AfterCapture(ctx)

			// Assert
			if tt.wantBeforeErr == "" {
				assert.NoError(t, beforeErr)
			} else {
				assert.EqualError(t, beforeErr, tt.wantBeforeErr)
			}
			assert.Equal(t, tt.wantOutcome, outcome)
			if tt.wantAfterErr == "" {
				assert.NoError(t, afterErr)
			} else {
				assert.EqualError(t, afterErr, tt.wantAfterErr)
			}
			assert.Equal(t, tt.wantWarnings, recorder.Msgs)
			assert.Equal(t, tt.wantInResult, recorder.InResult)
			runner.AssertExpectations(t)
			control.AssertExpectations(t)
		})
	}
}
