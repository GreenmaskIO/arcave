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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/composespec"
)

// sessionShowSpec is one service holding a named volume.
const sessionShowSpec = `
name: %s
services:
  db:
    image: ` + busyboxImage + `
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    volumes: ["data:/data"]
volumes:
  data: {}
`

// sessionShowEnv is an arcave project directory over a directory store.
type sessionShowEnv struct {
	projectRoot string
}

func newSessionShowEnv(t *testing.T) *sessionShowEnv {
	t.Helper()

	env := &sessionShowEnv{projectRoot: t.TempDir()}
	runArcave(t, env.projectRoot, nil, "init", "--name", "arcave-session-show-it")
	writeStorageConfigWith(t, env.projectRoot, startDirectoryStorage(t).file,
		storageE2EBackend{name: config.SnapshotBackendFullCopy, backend: config.SnapshotBackendFullCopy}, false)
	return env
}

// upComposeProject starts a compose project of one service holding a named volume, and names it.
func upComposeProject(t *testing.T) model.DockerComposeProjectName {
	t.Helper()

	compose := newImageRollbackEnv(t)
	project, err := composespec.NewCodec().Parse(compose.ctx, fmt.Appendf(nil, sessionShowSpec, compose.project))
	require.NoError(t, err)
	require.NoError(t, compose.compose.Create(compose.ctx, project, nil))
	require.NoError(t, compose.compose.Start(compose.ctx, project, 90*time.Second))
	return model.DockerComposeProjectName(compose.project)
}

func (e *sessionShowEnv) show(t *testing.T, args ...string) model.SessionDetail {
	t.Helper()

	command := append([]string{"session", "show", "--format", "json"}, args...)
	return decode[model.SessionDetail](t, runArcave(t, e.projectRoot, nil, command...))
}

// newShownNames is each shown compose project with its savepoints' names, in the order shown.
func newShownNames(detail model.SessionDetail) map[model.DockerComposeProjectName][]string {
	names := make(map[model.DockerComposeProjectName][]string, len(detail.ComposeProjects))
	for _, group := range detail.ComposeProjects {
		for _, savepoint := range group.Savepoints {
			names[group.ComposeProject] = append(names[group.ComposeProject], formatShownName(savepoint))
		}
	}
	return names
}

// formatShownName is a savepoint's name, a safety snapshot's spelled "safety".
func formatShownName(savepoint model.Savepoint) string {
	if savepoint.IsSafetySnapshot() {
		return "safety"
	}
	return savepoint.Name
}

func TestSessionShow_EmptySession(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionShowEnv(t)

	// Act
	detail := env.show(t)

	// Assert
	assert.Equal(t, model.DefaultSessionName, detail.Session.Name)
	assert.True(t, detail.IsCurrent())
	assert.NotNil(t, detail.ComposeProjects, "an empty session lists [], never null")
	assert.Empty(t, detail.ComposeProjects)
}

func TestSessionShow_GroupsTheSavepointsByComposeProject(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionShowEnv(t)
	first, second := upComposeProject(t), upComposeProject(t)
	runArcave(t, env.projectRoot, nil, "-p", first.String(), "savepoint", "create", "init")
	runArcave(t, env.projectRoot, nil, "-p", second.String(), "savepoint", "create", "seed")
	runArcave(t, env.projectRoot, nil, "-p", second.String(), "savepoint", "create", "migrated")

	// Act
	detail := env.show(t)

	// Assert
	assert.True(t, detail.IsCurrent())
	require.Len(t, detail.ComposeProjects, 2)
	assert.True(t, detail.ComposeProjects[0].ComposeProject < detail.ComposeProjects[1].ComposeProject,
		"compose projects by name")
	assert.Equal(t, map[model.DockerComposeProjectName][]string{
		first:  {"init"},
		second: {"migrated", "seed"},
	}, newShownNames(detail), "savepoints newest first")
}

func TestSessionShow_SafetySnapshotsIncluded(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionShowEnv(t)
	composeProject := upComposeProject(t)
	runArcave(t, env.projectRoot, nil, "-p", composeProject.String(), "savepoint", "create", "seed")
	runArcave(t, env.projectRoot, nil, "-p", composeProject.String(), "rollback", "seed")

	// Act
	detail := env.show(t)

	// Assert
	assert.Equal(t, map[model.DockerComposeProjectName][]string{composeProject: {"safety", "seed"}}, newShownNames(detail))
	assert.Equal(t, 1, detail.SavepointCount)
	assert.Equal(t, 1, detail.SafetySnapshotCount)
}

func TestSessionShow_ArgumentOutranksSessionFlag(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionShowEnv(t)
	runArcave(t, env.projectRoot, nil, "session", "create", "--name", "other")

	// Act
	named := env.show(t, "other")
	flagged := env.show(t, "--session", "other")
	both := env.show(t, "--session", "other", model.DefaultSessionName)
	typo := env.show(t, "--session", "typo", "other")

	// Assert: current is the session commands work in, --session included.
	assert.Equal(t, "other", named.Session.Name)
	assert.False(t, named.IsCurrent(), "the current session is baseline")
	assert.Equal(t, "other", flagged.Session.Name)
	assert.True(t, flagged.IsCurrent(), "--session names the session commands work in")
	assert.Equal(t, model.DefaultSessionName, both.Session.Name)
	assert.False(t, both.IsCurrent())
	assert.Equal(t, "other", typo.Session.Name)
	assert.False(t, typo.IsCurrent(), "a --session naming no session marks none, as session list does")
}

func TestSessionShow_NamesTheSessionItWasForkedFrom(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionShowEnv(t)
	runArcave(t, env.projectRoot, nil, "session", "create", "--name", "fork", "--from", model.DefaultSessionName)

	// Act
	forked := env.show(t, "fork")
	baseline := env.show(t)

	// Assert
	assert.Equal(t, &model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName}, forked.From)
	assert.Nil(t, baseline.From, "a session forked from none names no origin")
}

func TestSessionShow_NamesWhereTheCurrentSessionCameFrom(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionShowEnv(t)
	runArcave(t, env.projectRoot, nil, "session", "create", "--name", "other")
	show := func(environ []string, args ...string) model.SessionDetail {
		command := append([]string{"session", "show", "--format", "json"}, args...)
		return decode[model.SessionDetail](t, runArcave(t, env.projectRoot, environ, command...))
	}

	// Act
	byDefault := show(nil)
	byVariable := show([]string{"ARCAVE_SESSION=other"})
	byFlag := show([]string{"ARCAVE_SESSION=other"}, "--session", model.DefaultSessionName)
	runArcave(t, env.projectRoot, nil, "session", "set", "other")
	byPointer := show(nil)
	current := decode[model.CurrentSession](t, runArcave(t, env.projectRoot, []string{"ARCAVE_SESSION=baseline"},
		"session", "current", "--format", "json"))

	// Assert: an override names what it hides, unless it names that very session.
	baseline := &model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName}
	for _, tt := range []struct {
		detail model.SessionDetail
		name   string
		source model.CurrentSessionSource
	}{
		{detail: byDefault, name: model.DefaultSessionName,
			source: model.CurrentSessionSource{Source: model.SessionRefSourceDefault}},
		{detail: byVariable, name: "other",
			source: model.CurrentSessionSource{Source: model.SessionRefSourceEnv, Overrides: baseline}},
		{detail: byFlag, name: model.DefaultSessionName,
			source: model.CurrentSessionSource{Source: model.SessionRefSourceFlag}},
		{detail: byPointer, name: "other",
			source: model.CurrentSessionSource{Source: model.SessionRefSourcePointer}},
	} {
		assert.Equal(t, tt.name, tt.detail.Session.Name)
		assert.Equal(t, &tt.source, tt.detail.CurrentSource, tt.name)
	}
	assert.Equal(t, model.DefaultSessionName, current.Name)
	assert.Equal(t, model.CurrentSessionSource{
		Source:    model.SessionRefSourceEnv,
		Overrides: &model.SessionRef{ID: byPointer.Session.ID, Name: "other"},
	}, current.CurrentSource, "the variable hides the session session set made current")
}

func TestSessionShow_CurrentAndLatest(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionShowEnv(t)
	runArcave(t, env.projectRoot, nil, "session", "create", "--name", "older")
	runArcave(t, env.projectRoot, nil, "session", "create", "--name", "newer")
	runArcave(t, env.projectRoot, nil, "session", "set", "older")

	// Act
	none := env.show(t)
	current := env.show(t, model.RefCurrent)
	flagged := env.show(t, "--session", "newer", model.RefCurrent)
	latest := env.show(t, model.RefLatest)
	_, keywordErr := runArcaveWith(t, env.projectRoot, nil, "session", "show", "--session", model.RefCurrent)
	_, reservedErr := runArcaveWith(t, env.projectRoot, nil, "session", "create", "--name", model.RefLatest)
	runArcave(t, env.projectRoot, nil, "session", "create", "--name", "fork", "--from", model.RefCurrent)
	fork := env.show(t, "fork")

	// Assert
	assert.Equal(t, none, withCheckedAt(current, none.CheckedAt), "current is what a command naming none shows")
	assert.Equal(t, "newer", flagged.Session.Name, "current follows --session")
	assert.Equal(t, "newer", latest.Session.Name)
	assert.False(t, latest.IsCurrent())
	require.Error(t, keywordErr, "--session takes no keyword")
	require.Error(t, reservedErr, "a keyword is no session name")
	assert.Equal(t, "older", fork.From.Name, "--from current forks the current session")
}

// withCheckedAt is detail read at checkedAt, for comparing two reads.
func withCheckedAt(detail model.SessionDetail, checkedAt time.Time) model.SessionDetail {
	detail.CheckedAt = checkedAt
	return detail
}
