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

package cdc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// A stdio run is restic behind stdio.sh, copied in with the container, its
// repository the script's relay, with its bridge exec serving the runner's own repository.
func TestRunner_run_Stdio(t *testing.T) {
	// Arrange
	served := errors.New("served")
	server := &mockServer{}
	stream := &nopStream{}
	server.On("ServeRepository", mock.Anything, demoObjectID, stream).Return(served).Once()
	var serveErr error
	containers := &mockRunner{}
	containers.On("RunOneShot", mock.Anything, matchRestic("backup")).
		Run(func(args mock.Arguments) {
			spec := args.Get(1).(model.OneShotSpec)
			fmt.Fprint(spec.Exec.Stderr, "cat: broken pipe")
			serveErr = spec.Exec.Serve(context.Background(), stream)
		}).Return(nil).Once()
	logs := &strings.Builder{}
	ctx := zerolog.New(logs).Level(zerolog.DebugLevel).WithContext(context.Background())
	r := runner{
		containers: containers,
		image:      demoImage,
		password:   demoPassword,
		objectID:   demoObjectID,
		transport:  StdioTransport{Server: server},
	}

	// Act
	_, err := r.run(ctx, resticCmd{args: []string{"backup", "/m", "--json"}, binds: []string{"vol:/m:ro"}})

	// Assert
	require.NoError(t, err)
	specs := containers.Specs()
	require.Len(t, specs, 1)
	spec := specs[0]
	assert.Equal(t, demoImage, spec.Image)
	assert.Equal(t, []string{"/run/arcave/stdio.sh", "entrypoint"}, spec.Entrypoint)
	assert.Equal(t, []string{"backup", "/m", "--json"}, spec.Cmd)
	assert.Equal(t, []model.OneShotFile{
		{Path: "/run/arcave", Mode: fs.ModeDir | fs.ModeSticky | 0o777},
		{Path: "/run/arcave/password", Mode: 0o444, Content: []byte(demoPassword)},
		{Path: "/run/arcave/stdio.sh", Mode: 0o755, Content: stdioScript},
	}, spec.Files)
	assert.ElementsMatch(t,
		[]string{"RESTIC_REPOSITORY=rclone:arcave", "RESTIC_PASSWORD_FILE=/run/arcave/password"}, spec.Env)
	assert.Equal(t, []string{"vol:/m:ro"}, spec.Binds)
	assert.False(t, spec.HostNetwork)
	require.NotNil(t, spec.Exec)
	assert.Equal(t, []string{"/run/arcave/stdio.sh", "bridge"}, spec.Exec.Cmd)
	assert.ErrorIs(t, serveErr, served, "Serve must be the server's, over this runner's repository")
	assert.Contains(t, logs.String(), "cat: broken pipe", "the bridge's last stderr line, unterminated, must reach the debug log")
	containers.AssertExpectations(t)
	server.AssertExpectations(t)
}

// Every run of a stdio snapshot rides the exec bridge.
func TestSnapshotter_Snapshot_Stdio(t *testing.T) {
	// Arrange
	runner := &mockRunner{}
	runner.On("ResolveMountSource", mock.Anything, demoVolume()).Return(demoAddress, nil).Once()
	mock.InOrder(
		runner.On("RunOneShot", mock.Anything, matchRestic("init")).Return(nil).Once(),
		runner.On("RunOneShot", mock.Anything, matchRestic("backup")).
			Run(respondWith(backupSummaryLine)).Return(nil).Once(),
	)
	snapshotter, err := NewFactory(Dependencies{
		Runner:         runner,
		Transport:      StdioTransport{Server: &mockServer{}},
		RepositoryRoot: newStore(t),
		Options:        defaultOptions(),
	}).New(model.BackendSpec{Volume: demoVolume()})
	require.NoError(t, err)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err = snapshotter.Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: "019e843e-1650-7000-8000-000000000000"})

	// Assert
	require.NoError(t, err)
	specs := runner.Specs()
	require.Len(t, specs, 2)
	for i, spec := range specs {
		assert.True(t, isStdioSpec(spec), "run %d", i)
	}
	runner.AssertExpectations(t)
}

// An inspection over stdio runs the same restic arguments behind the bridge.
func TestInspector_ReadSnapshots_Stdio(t *testing.T) {
	// Arrange
	containers := &mockRunner{}
	containers.On("RunOneShot", mock.Anything, matchStdio([]string{"snapshots", "--json", "--no-lock"})).
		Run(respondWithOutput("[]\n", "")).Return(nil).Once()
	inspector := NewInspector(containers, StdioTransport{Server: &mockServer{}}, Options{Image: demoImage})

	// Act
	got, err := inspector.ReadSnapshots(zerolog.Nop().WithContext(context.Background()), demoObjectID)

	// Assert
	require.NoError(t, err)
	assert.Empty(t, got)
	containers.AssertExpectations(t)
}

// A forget over stdio runs the same restic arguments behind the bridge.
func TestPruner_Forget_Stdio(t *testing.T) {
	// Arrange
	containers := &mockRunner{}
	containers.On("RunOneShot", mock.Anything, matchStdio([]string{"prune"})).
		Run(respondWithOutput("total prune:           3 blobs / 2.000 KiB\n", "")).Return(nil).Once()
	pruner := NewPruner(containers, newStore(t), StdioTransport{Server: &mockServer{}}, Options{Image: demoImage})

	// Act
	got, err := pruner.Forget(zerolog.Nop().WithContext(context.Background()), demoObjectID, nil)

	// Assert
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int64(2048), *got)
	containers.AssertExpectations(t)
}

// matchStdio matches a stdio run of args in the demo image.
func matchStdio(args []string) any {
	return mock.MatchedBy(func(spec model.OneShotSpec) bool {
		return isStdioSpec(spec) && spec.Image == demoImage &&
			slices.Equal(spec.Cmd, args)
	})
}

// isStdioSpec reports whether spec reaches its repository over the exec bridge.
func isStdioSpec(spec model.OneShotSpec) bool {
	return spec.Exec != nil &&
		slices.Equal(spec.Exec.Cmd, []string{stdioScriptPath, "bridge"}) &&
		slices.Equal(spec.Entrypoint, []string{stdioScriptPath, "entrypoint"}) &&
		reflect.DeepEqual(spec.Files, newStdioFiles(DefaultPassword)) || reflect.DeepEqual(spec.Files, newStdioFiles(demoPassword)) &&
		slices.Contains(spec.Env, "RESTIC_REPOSITORY="+stdioRepository) &&
		!spec.HostNetwork
}

// nopStream is a stream nothing reads or writes.
type nopStream struct{}

func (*nopStream) Read([]byte) (int, error)    { return 0, io.EOF }
func (*nopStream) Write(p []byte) (int, error) { return len(p), nil }
func (*nopStream) Close() error                { return nil }
