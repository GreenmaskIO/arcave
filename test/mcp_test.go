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
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/lock"
)

// mcpCallTimeout bounds one tool call; a rollback restarts the compose project.
const mcpCallTimeout = 5 * time.Minute

var (
	arcaveBinOnce sync.Once
	arcaveBin     string
	arcaveBinErr  error
)

// buildArcave builds cmd/arcave once per run: the MCP tests drive the binary as an agent host would.
func buildArcave(t *testing.T) string {
	t.Helper()

	arcaveBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "arcave-mcp-it-")
		if err != nil {
			arcaveBinErr = err
			return
		}
		arcaveBin = filepath.Join(dir, "arcave")
		var out bytes.Buffer
		build := exec.Command("go", "build", "-o", arcaveBin, "./cmd/arcave")
		build.Dir = ".."
		build.Stdout, build.Stderr = &out, &out
		if err := build.Run(); err != nil {
			arcaveBinErr = err
			arcaveBin = out.String()
		}
	})
	require.NoError(t, arcaveBinErr, "build arcave: %s", arcaveBin)
	return arcaveBin
}

// arcaveCommand runs the binary in the session env's project root, unswayed by the caller's
// ARCAVE_* environment.
func arcaveCommand(t *testing.T, env *sessionEnv, args ...string) *exec.Cmd {
	t.Helper()

	cmd := exec.Command(buildArcave(t), args...)
	cmd.Dir = env.projectRoot
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ARCAVE_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	return cmd
}

// initArcave writes the config `arcave mcp` reads: the arcave project and the default storage
// newSessionEnv already opened, so the env's helpers see what the server writes.
func initArcave(t *testing.T, env *sessionEnv) {
	t.Helper()

	out, err := arcaveCommand(t, env, "init", "--name", sessionArcaveProject).CombinedOutput()
	require.NoError(t, err, "arcave init: %s", out)
}

// newMCPClient starts `arcave mcp args...` in the env's project root and connects to it over stdio.
func newMCPClient(t *testing.T, env *sessionEnv, args ...string) *mcpsdk.ClientSession {
	t.Helper()

	transport := &mcpsdk.CommandTransport{Command: arcaveCommand(t, env, append([]string{"mcp"}, args...)...)}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "arcave-it", Version: "0"}, nil)
	cs, err := client.Connect(env.ctx, transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callTool calls a tool that must succeed and decodes its structured result.
func callTool[T any](t *testing.T, cs *mcpsdk.ClientSession, name string, args map[string]any) T {
	t.Helper()

	res := callToolRaw(t, cs, name, args)
	require.False(t, res.IsError, "%s: %s", name, toolText(res))
	data, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var out T
	require.NoError(t, json.Unmarshal(data, &out))
	return out
}

// callToolErr calls a tool that must fail and returns what it told the agent.
func callToolErr(t *testing.T, cs *mcpsdk.ClientSession, name string, args map[string]any) string {
	t.Helper()

	res := callToolRaw(t, cs, name, args)
	require.True(t, res.IsError, "%s succeeded: %v", name, res.StructuredContent)
	return toolText(res)
}

func callToolRaw(t *testing.T, cs *mcpsdk.ClientSession, name string, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), mcpCallTimeout)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err, "%s", name)
	return res
}

func toolText(res *mcpsdk.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if text, ok := c.(*mcpsdk.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// pointerPath is the current-session pointer of the env's project root.
func (e *sessionEnv) pointerPath() string {
	return filepath.Join(e.projectRoot, model.StateDir, "current_session")
}

// readPointer is the id the pointer holds, "" when none is set.
func (e *sessionEnv) readPointer(t *testing.T) string {
	t.Helper()

	data, err := os.ReadFile(e.pointerPath())
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return strings.TrimSpace(string(data))
}

// currentNames are the names session_list marks current.
func currentNames(list sessionListResult) []string {
	var names []string
	for _, s := range list.Sessions {
		if s.Current {
			names = append(names, s.Name)
		}
	}
	return names
}

type sessionListResult struct {
	Sessions []model.SessionSummary `json:"sessions"`
}

type savepointListResult struct {
	Savepoints []model.Savepoint `json:"savepoints"`
}

func TestMCP_SessionLifecycle(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionEnv(t)
	initArcave(t, env)
	cs := newMCPClient(t, env)

	// Act & Assert: the default, a session made current, refused and forced deletes, back to the default.
	assert.Equal(t, model.DefaultSession, callTool[model.Session](t, cs, "session_current", nil).ID)

	feat := callTool[model.Session](t, cs, "session_create", map[string]any{"name": "feat"})
	got := callTool[model.Session](t, cs, "session_set", map[string]any{"session": "feat"})
	assert.Equal(t, feat.ID, got.ID)
	assert.Equal(t, feat.ID.String(), env.readPointer(t), "the pointer holds the id")
	assert.Equal(t, feat.ID, callTool[model.Session](t, cs, "session_current", nil).ID)
	assert.Equal(t, []string{"feat"}, currentNames(callTool[sessionListResult](t, cs, "session_list", nil)))

	assert.Contains(t, callToolErr(t, cs, "session_delete", map[string]any{"session": "feat"}),
		model.ErrCurrentSessionDelete.Error())
	assert.Contains(t, callToolErr(t, cs, "session_delete", map[string]any{"session": "", "force": true}),
		model.ErrNoSessionRef.Error(), "an empty ref never means the current session")
	assert.Contains(t, callToolErr(t, cs, "session_set", map[string]any{"session": ""}),
		model.ErrNoSessionRef.Error())

	deleted := callTool[model.DeleteSessionResponse](t, cs, "session_delete", map[string]any{"session": "feat", "force": true})
	assert.Equal(t, feat.ID, deleted.Session)
	assert.Empty(t, env.readPointer(t), "deleting the current session clears the pointer")
	assert.Equal(t, model.DefaultSession, callTool[model.Session](t, cs, "session_current", nil).ID)
}

func TestMCP_PinnedServer(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionEnv(t)
	initArcave(t, env)
	feat := env.create(t, "feat")
	cs := newMCPClient(t, env, "--session", "feat")

	// Act & Assert
	assert.Empty(t, env.readPointer(t), "a pin is no pointer")
	assert.Equal(t, feat.ID, callTool[model.Session](t, cs, "session_current", nil).ID)
	assert.Equal(t, []string{"feat"}, currentNames(callTool[sessionListResult](t, cs, "session_list", nil)),
		"the listing marks what the tools use")
	assert.Contains(t, callToolErr(t, cs, "session_set", map[string]any{"session": "baseline"}), "pinned")
	assert.Contains(t, callToolErr(t, cs, "session_unset", nil), "pinned")
	assert.Contains(t, callToolErr(t, cs, "session_delete", map[string]any{"session": "feat"}), "pinned")

	// The pin is the session, not its name: a new "feat" is not the pinned one.
	_, err := env.sessions.DeleteSession(env.ctx, model.DeleteSessionRequest{Session: "feat"})
	require.NoError(t, err)
	env.create(t, "feat")
	assert.Contains(t, callToolErr(t, cs, "session_current", nil), feat.ID.String())
}

func TestMCP_DanglingPointer(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionEnv(t)
	initArcave(t, env)
	gone := model.NewSessionID()
	require.NoError(t, os.WriteFile(env.pointerPath(), []byte(gone.String()+"\n"), 0o644))
	cs := newMCPClient(t, env)

	// Act & Assert
	msg := callToolErr(t, cs, "session_current", nil)
	assert.Contains(t, msg, gone.String())
	assert.Contains(t, msg, "session unset")
	assert.NotEmpty(t, callTool[sessionListResult](t, cs, "session_list", nil).Sessions, "the listing still answers")

	assert.Equal(t, model.DefaultSession, callTool[model.Session](t, cs, "session_unset", nil).ID)
	assert.Equal(t, model.DefaultSession, callTool[model.Session](t, cs, "session_current", nil).ID)
}

func TestMCP_PinToMissingSessionFailsStart(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionEnv(t)
	initArcave(t, env)
	cmd := arcaveCommand(t, env, "mcp", "--session", "ghost")
	var stderr bytes.Buffer
	cmd.Stdin, cmd.Stderr = strings.NewReader(""), &stderr

	// Act
	err := cmd.Run()

	// Assert
	require.Error(t, err, "the server never serves")
	assert.Contains(t, stderr.String(), model.ErrSessionNotFound.Error())
}

func TestMCP_LockedCallNamesTheFork(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionEnv(t)
	initArcave(t, env)
	cs := newMCPClient(t, env)
	release, err := lock.New(env.projectRoot).Acquire(env.ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = release() })

	// Act
	msg := callToolErr(t, cs, "session_create", map[string]any{"name": "feat"})

	// Assert
	assert.Contains(t, msg, model.ErrProjectLocked.Error())
	assert.Contains(t, msg, "session fork")
}

func TestMCP_SavepointsFollowTheCurrentSession(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionEnv(t)
	env.withComposeProject(t)
	initArcave(t, env)
	cs := newMCPClient(t, env)
	project := map[string]any{"project": env.project}
	with := func(kv ...any) map[string]any {
		args := map[string]any{"project": env.project}
		for i := 0; i < len(kv); i += 2 {
			args[kv[i].(string)] = kv[i+1]
		}
		return args
	}

	// Act: a savepoint in baseline, a fork made current, a savepoint there, a rollback there.
	v1 := callTool[model.Savepoint](t, cs, "savepoint_create", with("name", "v1"))
	fork := callTool[model.Session](t, cs, "session_create", map[string]any{"name": "feat", "from": "baseline"})
	callTool[model.Session](t, cs, "session_set", map[string]any{"session": "feat"})
	v2 := callTool[model.Savepoint](t, cs, "savepoint_create", with("name", "v2"))
	inFork := callTool[savepointListResult](t, cs, "savepoint_list", project)
	inBaseline := callTool[savepointListResult](t, cs, "savepoint_list", with("session", "baseline"))
	rolledBack := callTool[model.RollbackResponse](t, cs, "rollback", with("ref", "v1"))

	// Assert: what the tools reported is what each session's store holds.
	savepointIDs := func(sps []model.Savepoint) []model.SavepointID {
		ids := make([]model.SavepointID, len(sps))
		for i, sp := range sps {
			ids[i] = sp.ID
		}
		return ids
	}
	assert.ElementsMatch(t, []model.SavepointID{v1.ID, v2.ID}, savepointIDs(inFork.Savepoints))
	assert.Equal(t, []model.SavepointID{v1.ID}, savepointIDs(inBaseline.Savepoints))
	assert.Equal(t, v1.ID, rolledBack.RolledBackTo.ID)

	forkStore, err := env.in(t, fork.ID).svc.ListSavepoints(env.ctx, model.ListRequest{})
	require.NoError(t, err)
	assert.ElementsMatch(t, []model.SavepointID{v1.ID, v2.ID}, savepointIDs(forkStore))
	baselineStore, err := env.in(t, model.DefaultSession).svc.ListSavepoints(env.ctx, model.ListRequest{})
	require.NoError(t, err)
	assert.Equal(t, []model.SavepointID{v1.ID}, savepointIDs(baselineStore))
}
