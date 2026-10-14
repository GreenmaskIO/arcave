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
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// mcpExitTimeout bounds how long the server may take to exit once its rollback is over.
const mcpExitTimeout = 2 * time.Minute

// mcpServer is `arcave mcp` with its stdio in the test's hands, so the test can close the
// server's stdin or signal it the way an agent host does.
type mcpServer struct {
	cmd    *exec.Cmd
	stdin  *os.File
	cs     *mcpsdk.ClientSession
	exited chan error
}

// startMCPServer starts `arcave mcp` in the env's project root over plain pipes: the
// SDK's CommandTransport keeps stdin, and Wait would close a StdoutPipe under the client.
func startMCPServer(t *testing.T, env *sessionEnv) *mcpServer {
	t.Helper()

	cmd := arcaveCommand(t, env, "mcp")
	stdinR, stdinW, err := os.Pipe()
	require.NoError(t, err)
	stdoutR, stdoutW, err := os.Pipe()
	require.NoError(t, err)
	cmd.Stdin, cmd.Stdout = stdinR, stdoutW
	require.NoError(t, cmd.Start())
	_ = stdinR.Close()
	_ = stdoutW.Close()

	s := &mcpServer{cmd: cmd, stdin: stdinW, exited: make(chan error, 1)}
	go func() { s.exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = stdinW.Close()
		_ = stdoutR.Close()
	})

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "arcave-it", Version: "0"}, nil)
	s.cs, err = client.Connect(env.ctx, &mcpsdk.IOTransport{Reader: stdoutR, Writer: stdinW}, nil)
	require.NoError(t, err)
	return s
}

// waitExit is the server's exit code once it has exited on its own.
func (s *mcpServer) waitExit(t *testing.T) int {
	t.Helper()

	select {
	case err := <-s.exited:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		require.NoError(t, err)
		return 0
	case <-time.After(mcpExitTimeout):
		require.FailNow(t, "arcave mcp did not exit")
		return -1
	}
}

// waitForPhase waits until a rollback has written its marker at phase.
func waitForPhase(t *testing.T, env *sessionEnv, phase model.InflightPhase) {
	t.Helper()

	require.Eventually(t, func() bool {
		marker, found, err := env.inflight.Get(env.ctx)
		return err == nil && found && marker.Phase == phase
	}, mcpCallTimeout, 20*time.Millisecond, "the rollback never reached %s", phase)
}

// oneShotContainers are the throwaway containers arcave left in the daemon.
func oneShotContainers(t *testing.T, env *sessionEnv) []string {
	t.Helper()

	containers, err := env.daemon.ContainerList(env.ctx, container.ListOptions{
		All: true, Filters: filters.NewArgs(filters.Arg("label", "arcave.oneshot")),
	})
	require.NoError(t, err)
	names := make([]string, 0, len(containers))
	for _, c := range containers {
		names = append(names, c.Names...)
	}
	return names
}

// An agent host stopping `arcave mcp` mid-rollback leaves a consistent compose project: a
// signal lets the rollback finish, a closed stdin cancels it into the revert, and either way
// the server exits only once the call is over, with no marker, safety snapshot or helper left.
func TestMCP_InterruptedRollback(t *testing.T) {
	tests := []struct {
		name string
		// end stops the server the way a host does, mid-rollback.
		end         func(t *testing.T, s *mcpServer)
		wantContent string // what the compose project holds once the server has exited
		wantToolErr string // "" when the rollback succeeds
		wantExit    int
	}{
		{
			name:        "SIGTERM lets the rollback finish",
			end:         func(t *testing.T, s *mcpServer) { require.NoError(t, s.cmd.Process.Signal(syscall.SIGTERM)) },
			wantContent: "v1",
			wantExit:    130,
		},
		{
			name:        "a closed stdin reverts the rollback",
			end:         func(t *testing.T, s *mcpServer) { require.NoError(t, s.stdin.Close()) },
			wantContent: "v2",
			wantToolErr: "interrupted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			env := newSessionEnv(t)
			env.withComposeProject(t)
			initArcave(t, env)
			app := env.in(t, model.DefaultSession)
			app.write(t, app.replicas(t)[0], "v1")
			srv := startMCPServer(t, env)
			callTool[model.Savepoint](t, srv.cs, "savepoint_create", map[string]any{"project": env.project, "name": "v1"})
			app.write(t, app.replicas(t)[0], "v2")

			results := make(chan *mcpsdk.CallToolResult, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), mcpCallTimeout)
				defer cancel()
				res, _ := srv.cs.CallTool(ctx, &mcpsdk.CallToolParams{
					Name: "rollback", Arguments: map[string]any{"project": env.project, "ref": "v1"},
				})
				results <- res
			}()
			waitForPhase(t, env, model.InflightRecreating)

			// Act
			tt.end(t, srv)
			exit := srv.waitExit(t)

			// Assert
			assert.Equal(t, tt.wantExit, exit)
			if res := <-results; res != nil {
				if tt.wantToolErr == "" {
					assert.False(t, res.IsError, toolText(res))
				} else {
					assert.True(t, res.IsError)
					assert.Contains(t, toolText(res), tt.wantToolErr)
				}
			}
			_, found, err := env.inflight.Get(env.ctx)
			require.NoError(t, err)
			assert.False(t, found, "the rollback must not leave its marker")
			replica := app.replicas(t)[0]
			assert.Equal(t, tt.wantContent, app.read(t, replica, volumeMarker), "the volume")
			assert.Equal(t, tt.wantContent, app.read(t, replica, layerMarker), "the writable layer")
			all, err := app.svc.ListSavepoints(env.ctx, model.ListRequest{IncludeSafetySnapshots: true})
			require.NoError(t, err)
			require.Len(t, all, 1, "the safety snapshot is dropped")
			assert.Equal(t, "v1", all[0].Name)
			assert.Empty(t, oneShotContainers(t, env), "no helper container is left behind")
		})
	}
}
