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

//go:build unix

package cdc

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stdioScriptTimeout bounds every wait on a script process, so a pump that never
// ends fails the test rather than hangs it.
const stdioScriptTimeout = 10 * time.Second

// newStdioScript writes stdio.sh into a fresh directory and returns its path,
// skipping the test on a host without sh.
func newStdioScript(t *testing.T) string {
	t.Helper()

	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on this host")
	}
	path := filepath.Join(t.TempDir(), "stdio.sh")
	require.NoError(t, os.WriteFile(path, stdioScript, 0o755))
	return path
}

// newRoleCmd is the script in role, in its own process group so cleanup takes the
// role's background copies with it.
func newRoleCmd(t *testing.T, script string, args ...string) *exec.Cmd {
	t.Helper()

	cmd := exec.Command("sh", append([]string{script}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	})
	return cmd
}

// startRole starts the script in role, its stdin and stdout piped to the test.
func startRole(t *testing.T, script string, args ...string) (*exec.Cmd, io.WriteCloser, io.Reader) {
	t.Helper()

	cmd := newRoleCmd(t, script, args...)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	return cmd, stdin, stdout
}

// waitWithin waits for cmd, failing the test if it does not exit in time.
func waitWithin(t *testing.T, cmd *exec.Cmd) error {
	t.Helper()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(stdioScriptTimeout):
		t.Fatalf("%v did not exit", cmd.Args)
		return nil
	}
}

// readWithin reads exactly n bytes off r, failing the test if they do not come in time.
func readWithin(t *testing.T, r io.Reader, n int) string {
	t.Helper()

	got := make(chan string, 1)
	go func() {
		buf := make([]byte, n)
		k, _ := io.ReadFull(r, buf)
		got <- string(buf[:k])
	}()
	select {
	case s := <-got:
		return s
	case <-time.After(stdioScriptTimeout):
		t.Fatalf("no %d bytes came", n)
		return ""
	}
}

// requireFIFOs fails the test unless dir holds both FIFOs.
func requireFIFOs(t *testing.T, dir string) {
	t.Helper()

	for _, name := range []string{"req", "resp"} {
		info, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err)
		require.Equal(t, os.ModeNamedPipe, info.Mode().Type(), name)
	}
}

// The entrypoint makes the FIFOs on its own and execs restic with the script as
// its rclone program, restic's own arguments after.
func TestStdioScript_entrypoint(t *testing.T) {
	// Arrange: a restic on PATH that prints the arguments it was given.
	script := newStdioScript(t)
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "restic"), []byte("#!/bin/sh\necho \"$@\"\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := newRoleCmd(t, script, "entrypoint", "backup", "/m", "--json")

	// Act
	out, err := cmd.Output()

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "-o rclone.program="+script+" -o rclone.args=relay backup /m --json", strings.TrimSpace(string(out)))
	requireFIFOs(t, filepath.Dir(script))
}

// The relay and the bridge are the two halves of one pipe: what restic writes to
// the relay comes out of the bridge, what arcave writes to the bridge comes out
// of the relay, and each ends with its stdin. Started at once, either may make the FIFOs.
func TestStdioScript_relayAndBridge(t *testing.T) {
	// Arrange
	script := newStdioScript(t)
	bridge, bridgeIn, bridgeOut := startRole(t, script, "bridge")
	relay, relayIn, relayOut := startRole(t, script, "relay", "arcave")

	// Act
	_, requestErr := io.WriteString(relayIn, "request")
	require.NoError(t, requestErr)
	request := readWithin(t, bridgeOut, len("request"))
	_, responseErr := io.WriteString(bridgeIn, "response")
	require.NoError(t, responseErr)
	response := readWithin(t, relayOut, len("response"))
	require.NoError(t, relayIn.Close())
	relayErr := waitWithin(t, relay)
	require.NoError(t, bridgeIn.Close())
	bridgeErr := waitWithin(t, bridge)

	// Assert
	assert.Equal(t, "request", request, "restic's request must reach arcave")
	assert.Equal(t, "response", response, "arcave's response must reach restic")
	assert.NoError(t, relayErr, "the relay ends with restic's stdin")
	assert.NoError(t, bridgeErr, "the bridge ends with arcave's stream")
	requireFIFOs(t, filepath.Dir(script))
}

// A run the script cannot serve fails with its own message and status.
func TestStdioScript_failures(t *testing.T) {
	tests := []struct {
		name       string
		arrange    func(t *testing.T, dir string)
		relative   bool
		args       []string
		wantStatus int
		wantOutput string
	}{
		{name: "an unknown role", args: []string{"serve"}, wantStatus: 2, wantOutput: "unknown role 'serve'"},
		{name: "no role", wantStatus: 2, wantOutput: "unknown role ''"},
		{
			name:       "a relative path, which places no FIFO",
			relative:   true,
			args:       []string{"bridge"},
			wantStatus: 2,
			wantOutput: "run by absolute path, not 'stdio.sh'",
		},
		{
			name: "a regular file where a FIFO goes",
			arrange: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "req"), nil, 0o600))
			},
			args:       []string{"bridge"},
			wantStatus: 1,
			wantOutput: "cannot make the FIFOs",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			script := newStdioScript(t)
			if tt.arrange != nil {
				tt.arrange(t, filepath.Dir(script))
			}
			cmd := newRoleCmd(t, script, tt.args...)
			if tt.relative {
				cmd.Dir, cmd.Args[1] = filepath.Dir(script), filepath.Base(script)
			}

			// Act
			out, err := cmd.CombinedOutput()

			// Assert
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr)
			assert.Equal(t, tt.wantStatus, exitErr.ExitCode())
			assert.Contains(t, string(out), tt.wantOutput)
		})
	}
}
