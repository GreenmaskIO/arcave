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
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// progressLine is one line of the --progress=json stream.
type progressLine struct {
	Type   string          `json:"type"`
	Event  *progress.Event `json:"event"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

// progressStream is a --progress=json run read back: the tree rebuilt by open.parent, and the
// line it ended on.
type progressStream struct {
	kinds map[progress.Kind]bool
	// settled are the kinds of the nodes a resource settled under: compose's containers.
	settled map[progress.Kind]bool
	// unended are the nodes that opened and never reached a terminal status.
	unended []progress.NodeID
	// orphans are the nodes opened under a parent the stream had not opened.
	orphans []progress.NodeID
	root    progress.Status
	last    progressLine
}

func readProgressStream(t *testing.T, out []byte) progressStream {
	t.Helper()

	var (
		kinds   = map[progress.Kind]bool{}
		settled = map[progress.Kind]bool{}
		nodes   = map[progress.NodeID]progress.Kind{}
		open    = map[progress.NodeID]bool{}
		ended   = map[progress.NodeID]bool{}
		order   []progress.NodeID
		orphans []progress.NodeID
		root    progress.Status
		last    progressLine
	)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for scanner.Scan() {
		var line progressLine
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &line), "line %s", scanner.Bytes())
		last = line
		if line.Type != "progress" {
			continue
		}
		evt := line.Event
		require.NotNil(t, evt, "a progress line carries its event")
		if evt.Open != nil {
			if evt.Open.Parent != 0 && !open[evt.Open.Parent] {
				orphans = append(orphans, evt.Node)
			}
			open[evt.Node] = true
			order = append(order, evt.Node)
			kinds[evt.Open.Step.Kind] = true
			nodes[evt.Node] = evt.Open.Step.Kind
		}
		if evt.Resource != nil && evt.Settled {
			settled[nodes[evt.Node]] = true
		}
		if evt.Status.IsTerminal() {
			ended[evt.Node] = true
			if evt.Node == 1 {
				root = evt.Status
			}
		}
	}
	require.NoError(t, scanner.Err())
	var unended []progress.NodeID
	for _, node := range order {
		if !ended[node] {
			unended = append(unended, node)
		}
	}
	return progressStream{
		kinds:   kinds,
		settled: settled,
		unended: unended,
		orphans: orphans,
		root:    root,
		last:    last,
	}
}

// TestProgressE2E_JSONStream drives the built binary with --progress=json: every node of the
// stream ends, every one opens under a node already open, and the result is the last line.
func TestProgressE2E_JSONStream(t *testing.T) {
	t.Parallel()

	// Arrange
	compose := newImageRollbackEnv(t)
	projectRoot := t.TempDir()
	runArcave(t, projectRoot, nil, "init", "--name", "arcave-progress-e2e")
	writeStorageConfig(t, projectRoot, startDirectoryStorage(t).file)
	compose.up(t, 1)
	run := func(args ...string) []byte {
		t.Helper()
		return runArcave(t, projectRoot, nil, append([]string{"-p", compose.project, "--progress", "json"}, args...)...)
	}

	// Act
	created := readProgressStream(t, run("savepoint", "create", "sp1"))
	rolledBack := readProgressStream(t, run("rollback", "sp1"))
	refusal := arcaveCommandIn(t, projectRoot, "-p", compose.project, "--progress", "json", "rollback", "missing")
	refusedOut, refusedErr := refusal.Output()

	// Assert
	for name, stream := range map[string]progressStream{"savepoint create": created, "rollback": rolledBack} {
		assert.Empty(t, stream.unended, "%s: every node ends", name)
		assert.Empty(t, stream.orphans, "%s: every node opens under one already open", name)
		assert.Equal(t, progress.StatusSucceeded, stream.root, "%s: the root succeeds", name)
		assert.Equal(t, "result", stream.last.Type, "%s: the result is the last line", name)
		assert.Empty(t, stream.last.Error, name)
	}
	for _, kind := range []progress.Kind{
		"savepoint.create", "project.introspect", "compose.stop",
		"savepoint.capture", "volume.capture", "volume", "layer.capture", "layer", "compose.start",
	} {
		assert.True(t, created.kinds[kind], "savepoint create reports %s", kind)
	}
	assert.False(t, created.kinds["session.lock"], "a free session lock is not reported")
	assert.True(t, created.settled["compose.stop"], "compose's containers settle under compose.stop")
	assert.True(t, rolledBack.settled["compose.create"], "and under compose.create")
	for _, kind := range []progress.Kind{
		"rollback", "rollback.prepare", "layer.restore", "rollback.safety-snapshot", "savepoint.capture",
		"rollback.apply", "compose.down", "compose.create", "volume.restore", "compose.start", "rollback.cleanup",
	} {
		assert.True(t, rolledBack.kinds[kind], "rollback reports %s", kind)
	}
	savepoint := decode[model.Savepoint](t, created.last.Result)
	assert.Equal(t, "sp1", savepoint.Name)
	result := decode[model.RollbackResponse](t, rolledBack.last.Result)
	assert.Equal(t, savepoint.ID, result.RolledBackTo.ID)

	var exitErr *exec.ExitError
	require.True(t, errors.As(refusedErr, &exitErr), "the refusal exits non-zero: %v", refusedErr)
	assert.Equal(t, 1, exitErr.ExitCode())
	refused := readProgressStream(t, refusedOut)
	assert.Equal(t, "error", refused.last.Type, "the error is the last line")
	assert.Contains(t, refused.last.Error, "missing")
	assert.Equal(t, progress.StatusFailed, refused.root)
	assert.Empty(t, refused.unended)
}
