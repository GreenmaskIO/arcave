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

package test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// hooksSpec is one busybox service of %[2]d replicas over a named volume, its labels %[3]s.
const hooksSpec = `
name: %[1]s
services:
  app:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    deploy:
      replicas: %[2]d
    labels:
      arcave.image.capture: "false"
%[3]s
    volumes:
      - data:/data
volumes:
  data: {}
`

// hooksEnv is a compose project of hooksSpec and an arcave project over it, driven by the binary.
type hooksEnv struct {
	*imageRollbackEnv
	root string
}

// arcaveRun is how one run of the binary ended.
type arcaveRun struct {
	stdout, stderr []byte
	exitCode       int
	elapsed        time.Duration
}

func newHooksEnv(t *testing.T, replicas int, labels map[string]string) *hooksEnv {
	t.Helper()

	env := newImageRollbackEnv(t)
	var rendered []string
	for _, key := range slices.Sorted(maps.Keys(labels)) {
		rendered = append(rendered, fmt.Sprintf("      %s: %s", strconv.Quote(key), strconv.Quote(labels[key])))
	}
	env.upDocument(t, fmt.Sprintf(hooksSpec, env.project, replicas, strings.Join(rendered, "\n")))
	root := t.TempDir()
	runArcave(t, root, nil, "init", "--name", env.project)
	writeStorageConfig(t, root, startDirectoryStorage(t).file, config.SnapshotBackendFullCopy)
	return &hooksEnv{
		imageRollbackEnv: env,
		root:             root,
	}
}

// arcave runs the binary on the compose project, whatever it exits with.
func (e *hooksEnv) arcave(t *testing.T, env []string, args ...string) arcaveRun {
	t.Helper()

	cmd := arcaveCommandIn(t, e.root, append([]string{"-p", e.project}, args...)...)
	cmd.Env = append(cmd.Env, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	started := time.Now()
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil {
		require.True(t, errors.As(err, &exitErr), "arcave %v: %v", args, err)
	}
	return arcaveRun{
		stdout:   stdout.Bytes(),
		stderr:   stderr.Bytes(),
		exitCode: cmd.ProcessState.ExitCode(),
		elapsed:  time.Since(started),
	}
}

// savepoints are the savepoints `savepoint list` shows.
func (e *hooksEnv) savepoints(t *testing.T) []model.Savepoint {
	t.Helper()

	run := e.arcave(t, nil, "savepoint", "list")
	require.Zero(t, run.exitCode, "%s", run.stderr)
	var savepoints []model.Savepoint
	require.NoError(t, json.Unmarshal(run.stdout, &savepoints), "%s", run.stdout)
	return savepoints
}

// replica is the container of replica n of app.
func (e *hooksEnv) replica(t *testing.T, n int) string {
	t.Helper()

	replica, ok := e.containers(t)["app-"+strconv.Itoa(n)]
	require.True(t, ok, "app-%d runs", n)
	return replica.ID
}

// hookStream is a --progress json run read back: the warnings, the kinds opened, and the result.
type hookStream struct {
	warned    []progress.Event
	kinds     []progress.Kind
	savepoint model.Savepoint
}

func readHookStream(t *testing.T, out []byte) hookStream {
	t.Helper()

	var (
		warned    []progress.Event
		kinds     []progress.Kind
		savepoint model.Savepoint
	)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for scanner.Scan() {
		var line progressLine
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &line), "line %s", scanner.Bytes())
		switch {
		case line.Type == "result":
			require.NoError(t, json.Unmarshal(line.Result, &savepoint))
		case line.Event == nil:
		case line.Event.Open != nil:
			kinds = append(kinds, line.Event.Open.Step.Kind)
		case line.Event.Status == progress.StatusWarned:
			warned = append(warned, *line.Event)
		}
	}
	require.NoError(t, scanner.Err())
	return hookStream{
		warned:    warned,
		kinds:     kinds,
		savepoint: savepoint,
	}
}

// warnedMsgs are the messages of the warnings.
func (s hookStream) warnedMsgs() []string {
	msgs := make([]string, 0, len(s.warned))
	for _, evt := range s.warned {
		msgs = append(msgs, evt.Msg)
	}
	return msgs
}

// The pre-capture hook runs before the capture and the post-capture one after it: what the first
// wrote is in the savepoint, and gone from the live project.
func TestSavepoint_HooksRunAroundCapture(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newHooksEnv(t, 1, map[string]string{
		"arcave.hook.pre-capture":  "sh -c 'echo held > /data/marker'",
		"arcave.hook.post-capture": "rm /data/marker",
	})

	// Act
	created := env.arcave(t, nil, "savepoint", "create", "sp1")
	afterCreate := env.readOrAbsent(t, env.replica(t, 1), "/data/marker")
	rolledBack := env.arcave(t, nil, "rollback", "sp1")

	// Assert
	require.Zero(t, created.exitCode, "%s", created.stderr)
	assert.Equal(t, absent, afterCreate, "the post-capture hook ran")
	require.Zero(t, rolledBack.exitCode, "%s", rolledBack.stderr)
	assert.Equal(t, "held\n", env.read(t, env.replica(t, 1), "/data/marker"), "the savepoint holds what the pre-capture hook wrote")
}

// A failed pre-capture hook captures nothing and stops nothing; the post-capture hooks still run.
func TestSavepoint_PreHookFailureCapturesNothing(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newHooksEnv(t, 1, map[string]string{
		"arcave.hook.pre-capture":  "sh -c 'echo not ready >&2; exit 3'",
		"arcave.hook.post-capture": "touch /data/post-ran",
	})
	before := env.inspect(t, env.replica(t, 1))

	// Act
	run := env.arcave(t, nil, "savepoint", "create", "sp1")
	listed := env.savepoints(t)
	after := env.inspect(t, env.replica(t, 1))
	postRan := env.readOrAbsent(t, env.replica(t, 1), "/data/post-ran")

	// Assert
	assert.Equal(t, 1, run.exitCode)
	assert.Contains(t, string(run.stderr), "pre-capture hook of app-1 failed: exit 3: not ready")
	assert.Empty(t, listed)
	assert.True(t, after.State.Running)
	assert.Equal(t, before.State.StartedAt, after.State.StartedAt, "nothing was stopped")
	assert.NotEqual(t, absent, postRan, "the post-capture hook ran")
}

// A pre-capture failure on-error continue lets through is a warning of the stream the result
// repeats, and the savepoint records it.
func TestSavepoint_PreHookContinue(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newHooksEnv(t, 1, map[string]string{
		"arcave.hook.pre-capture":          "sh -c 'echo boom >&2; exit 3'",
		"arcave.hook.pre-capture.on-error": "continue",
	})

	// Act
	run := env.arcave(t, nil, "--progress", "json", "savepoint", "create", "sp1")

	// Assert
	require.Zero(t, run.exitCode, "%s", run.stderr)
	stream := readHookStream(t, run.stdout)
	i := slices.IndexFunc(stream.warned, func(evt progress.Event) bool {
		return evt.Msg == "pre-capture hook of app-1 failed: exit 3: boom; continuing"
	})
	require.GreaterOrEqual(t, i, 0, "warned: %v", stream.warnedMsgs())
	assert.True(t, stream.warned[i].InResult)
	require.NotNil(t, stream.savepoint.NotCovered)
	require.Len(t, stream.savepoint.NotCovered.FailedHooks, 1)
	assert.Equal(t, model.FailedHook{
		Service:   "app",
		Replica:   1,
		Container: env.project + "-app-1",
		Hook:      model.HookPhasePreCapture,
		ExitCode:  new(3),
		Error:     "exit 3: boom",
	}, stream.savepoint.NotCovered.FailedHooks[0])
}

// The failure let through is in the result, as JSON and as text, and in the list.
func TestSavepoint_PreHookContinueInResult(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newHooksEnv(t, 1, map[string]string{
		"arcave.hook.pre-capture": "sh -c 'echo boom >&2; exit 3'",
	})
	continuing := []string{"ARCAVE_CAPTURE_HOOKS_ON_ERROR=continue"}

	// Act
	asJSON := env.arcave(t, continuing, "savepoint", "create", "sp1")
	asText := env.arcave(t, continuing, "--format", "text", "savepoint", "create", "sp2")
	listed := env.savepoints(t)

	// Assert
	require.Zero(t, asJSON.exitCode, "%s", asJSON.stderr)
	var savepoint model.Savepoint
	require.NoError(t, json.Unmarshal(asJSON.stdout, &savepoint))
	require.NotNil(t, savepoint.NotCovered)
	require.Len(t, savepoint.NotCovered.FailedHooks, 1)
	assert.Equal(t, new(3), savepoint.NotCovered.FailedHooks[0].ExitCode)
	require.Zero(t, asText.exitCode, "%s", asText.stderr)
	assert.Contains(t, string(asText.stdout), "app-1: pre-capture hook failed (exit 3: boom), captured anyway")
	assert.Contains(t, string(asText.stdout), "fix the hook, or set arcave.hook.pre-capture.on-error: fail")
	require.Len(t, listed, 2)
	for _, sp := range listed {
		require.NotNil(t, sp.NotCovered, sp.Name)
		assert.Len(t, sp.NotCovered.FailedHooks, 1, sp.Name)
	}
}

// A failed post-capture hook fails the command past the commit, naming the savepoint it kept.
func TestSavepoint_PostHookFailureNamesSavepoint(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newHooksEnv(t, 1, map[string]string{
		"arcave.hook.post-capture": "sh -c 'echo lock held >&2; exit 1'",
	})

	// Act
	run := env.arcave(t, nil, "savepoint", "create", "sp1")
	listed := env.savepoints(t)

	// Assert
	assert.Equal(t, 1, run.exitCode)
	require.Len(t, listed, 1)
	assert.Contains(t, string(run.stderr), fmt.Sprintf(
		"savepoint %s committed; bringing the project back: post-capture hook of app-1 failed: exit 1: lock held",
		listed[0].ID))
}

// A post-capture failure let through is a warning on stderr; the record, committed before it,
// does not carry it.
func TestSavepoint_PostHookContinueWarns(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newHooksEnv(t, 1, map[string]string{
		"arcave.hook.post-capture":          "sh -c 'echo lock held >&2; exit 1'",
		"arcave.hook.post-capture.on-error": "continue",
	})

	// Act
	run := env.arcave(t, nil, "savepoint", "create", "sp1")

	// Assert
	require.Zero(t, run.exitCode, "%s", run.stderr)
	assert.Contains(t, string(run.stderr), "post-capture hook of app-1 failed: exit 1: lock held")
	var savepoint model.Savepoint
	require.NoError(t, json.Unmarshal(run.stdout, &savepoint))
	assert.True(t, savepoint.NotCovered == nil || len(savepoint.NotCovered.FailedHooks) == 0,
		"a post-capture failure is not in the record")
}

// At debug a hook's command and the tail of its stdout are logged.
func TestSavepoint_HookStdoutAtDebug(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newHooksEnv(t, 1, map[string]string{
		"arcave.hook.pre-capture": "echo hello",
	})

	// Act
	run := env.arcave(t, nil, "--log-level", "debug", "--log-format", "json", "savepoint", "create", "sp1")

	// Assert
	require.Zero(t, run.exitCode, "%s", run.stderr)
	var finished map[string]any
	for line := range strings.Lines(string(run.stderr)) {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil && record["message"] == "hook finished" {
			finished = record
		}
	}
	require.NotNil(t, finished, "%s", run.stderr)
	assert.Equal(t, "hello\n", finished["stdout"])
	assert.Equal(t, "pre-capture", finished["hook"])
	assert.Equal(t, "app", finished["service"])
	assert.Contains(t, string(run.stderr), `"command":["echo","hello"]`)
}

// A hook past its timeout fails the command in about that time; the process is left to run.
func TestSavepoint_HookTimeout(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newHooksEnv(t, 1, map[string]string{
		"arcave.hook.pre-capture":         "sleep 30",
		"arcave.hook.pre-capture.timeout": "1s",
	})

	// Act
	run := env.arcave(t, nil, "savepoint", "create", "sp1")

	// Assert
	assert.Equal(t, 1, run.exitCode)
	assert.Contains(t, string(run.stderr), "pre-capture hook of app-1 failed: timed out after 1s")
	assert.Less(t, run.elapsed, 20*time.Second)
}

// .user runs the hook as another user.
func TestSavepoint_HookUser(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newHooksEnv(t, 1, map[string]string{
		"arcave.hook.pre-capture":      "sh -c 'id -u > /tmp/uid'",
		"arcave.hook.pre-capture.user": "nobody",
	})

	// Act
	run := env.arcave(t, nil, "savepoint", "create", "sp1")
	uid := env.readOrAbsent(t, env.replica(t, 1), "/tmp/uid")

	// Assert
	require.Zero(t, run.exitCode, "%s", run.stderr)
	assert.Equal(t, "65534", strings.TrimSpace(uid))
}

// A replica frozen on entry skips its pre-capture hook with a warning, and runs its post-capture
// one once it is back.
func TestSavepoint_PreHookSkippedOnPausedReplica(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newHooksEnv(t, 2, map[string]string{
		"arcave.hook.pre-capture":  "touch /tmp/pre",
		"arcave.hook.post-capture": "touch /tmp/post",
	})
	require.NoError(t, env.engine().PauseContainers(env.ctx, []string{env.replica(t, 2)}))

	// Act
	run := env.arcave(t, nil, "--progress", "json", "savepoint", "create", "sp1")
	ran := map[string]string{}
	for _, file := range []string{"1/tmp/pre", "1/tmp/post", "2/tmp/pre", "2/tmp/post"} {
		n, _ := strconv.Atoi(file[:1])
		ran[file] = env.readOrAbsent(t, env.replica(t, n), file[1:])
	}

	// Assert
	require.Zero(t, run.exitCode, "%s", run.stderr)
	assert.Contains(t, readHookStream(t, run.stdout).warnedMsgs(), "app-2 was paused: pre-capture hook skipped")
	assert.NotEqual(t, absent, ran["1/tmp/pre"])
	assert.NotEqual(t, absent, ran["1/tmp/post"])
	assert.Equal(t, absent, ran["2/tmp/pre"], "the frozen replica's pre-capture hook was skipped")
	assert.NotEqual(t, absent, ran["2/tmp/post"], "and its post-capture hook ran once it was back")
}

// Under pause the pre-capture hook runs before the freeze and the post-capture one after the
// thaw: an exec into a frozen replica would be skipped. Nothing restarts.
func TestSavepoint_HooksWithPauseMode(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newHooksEnv(t, 1, map[string]string{
		"arcave.capture.mode":      "pause",
		"arcave.hook.pre-capture":  "touch /tmp/pre",
		"arcave.hook.post-capture": "touch /tmp/post",
	})
	before := env.inspect(t, env.replica(t, 1))

	// Act
	run := env.arcave(t, nil, "--progress", "json", "savepoint", "create", "sp1")
	pre := env.readOrAbsent(t, env.replica(t, 1), "/tmp/pre")
	post := env.readOrAbsent(t, env.replica(t, 1), "/tmp/post")
	after := env.inspect(t, env.replica(t, 1))

	// Assert
	require.Zero(t, run.exitCode, "%s", run.stderr)
	for _, msg := range readHookStream(t, run.stdout).warnedMsgs() {
		assert.NotContains(t, msg, "hook", "no hook was skipped or failed")
	}
	assert.NotEqual(t, absent, pre)
	assert.NotEqual(t, absent, post)
	assert.Equal(t, before.State.StartedAt, after.State.StartedAt)
	assert.False(t, after.State.Paused)
}

// A rollback runs no hook, its safety snapshot included.
func TestRollback_RunsNoHooks(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newHooksEnv(t, 1, map[string]string{
		"arcave.hook.pre-capture":  "true",
		"arcave.hook.post-capture": "true",
	})
	created := env.arcave(t, nil, "savepoint", "create", "sp1")
	require.Zero(t, created.exitCode, "%s", created.stderr)

	// Act
	run := env.arcave(t, nil, "--progress", "json", "rollback", "sp1")

	// Assert
	require.Zero(t, run.exitCode, "%s", run.stderr)
	stream := readHookStream(t, run.stdout)
	assert.Contains(t, stream.kinds, progress.Kind("savepoint.capture"), "the safety snapshot was taken")
	for _, kind := range stream.kinds {
		assert.False(t, strings.HasPrefix(string(kind), "hook"), "a rollback opened %s", kind)
	}
}

// A bad hook label refuses the savepoint before anything is stopped.
func TestSavepoint_InvalidHookLabelStopsNothing(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newHooksEnv(t, 1, map[string]string{
		"arcave.hook.pre-capture":         "true",
		"arcave.hook.pre-capture.timeout": "5",
	})
	before := env.inspect(t, env.replica(t, 1))

	// Act
	run := env.arcave(t, nil, "savepoint", "create", "sp1")
	after := env.inspect(t, env.replica(t, 1))

	// Assert
	assert.Equal(t, 1, run.exitCode)
	assert.Contains(t, string(run.stderr),
		`arcave.hook.pre-capture.timeout="5" on service app: want a positive Go duration, e.g. 30s: time: missing unit in duration "5"`)
	assert.Equal(t, before.State.StartedAt, after.State.StartedAt)
}

// An exec keeps its streams apart and reports its exit; a replica frozen, stopped or gone is
// model.ErrContainerNotRunning, whichever engine says it how.
func TestEngine_ExecInContainer(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newHooksEnv(t, 3, nil)
	engine := newRuntimeProfile(t, env.daemon).engine
	running, paused, stopped := env.replica(t, 1), env.replica(t, 2), env.replica(t, 3)
	require.NoError(t, engine.PauseContainers(env.ctx, []string{paused}))
	t.Cleanup(func() { _ = engine.UnpauseContainers(env.ctx, []string{paused}) })
	_, err := env.daemon.ContainerStop(env.ctx, stopped, client.ContainerStopOptions{})
	require.NoError(t, err)

	// Act
	result, runErr := engine.ExecInContainer(env.ctx, running, model.ExecSpec{
		Cmd:  []string{"sh", "-c", "echo out; echo err >&2; exit 3"},
		User: "nobody",
	})
	_, pausedErr := engine.ExecInContainer(env.ctx, paused, model.ExecSpec{Cmd: []string{"true"}})
	_, stoppedErr := engine.ExecInContainer(env.ctx, stopped, model.ExecSpec{Cmd: []string{"true"}})
	_, goneErr := engine.ExecInContainer(env.ctx, "arcave-no-such-container", model.ExecSpec{Cmd: []string{"true"}})

	// Assert
	require.NoError(t, runErr)
	assert.Equal(t, model.ExecResult{ExitCode: 3, Stdout: []byte("out\n"), Stderr: []byte("err\n")}, result)
	assert.ErrorIs(t, pausedErr, model.ErrContainerNotRunning)
	assert.ErrorIs(t, stoppedErr, model.ErrContainerNotRunning)
	assert.ErrorIs(t, goneErr, model.ErrContainerNotRunning)
}
