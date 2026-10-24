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

	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/greenmaskio/storages/directory"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
)

// The restic 0.19.1 output the inspector parses, captured verbatim from a repository of three
// snapshots: two tagged by arcave, one not.
const (
	snapshotsFixture = "testdata/snapshots.json"
	// forgetFixture is the stdout of forget <two ids> --prune --dry-run --no-lock: prune has no
	// --json output, and forget --json prints nothing at all for ids.
	forgetFixture = "testdata/forget-prune-dry-run.txt"
)

var (
	forgottenSnapshot = strings.Repeat("0", 63) + "a"
	otherSnapshot     = strings.Repeat("0", 63) + "b"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(name)
	require.NoError(t, err)
	return string(body)
}

// matchInspection matches the one run of an inspection: args, against demoObjectID's repository,
// in the image and network the options name. One expected run is also no lock cleared.
func matchInspection(args []string, hostNetwork bool) any {
	return mock.MatchedBy(func(spec model.OneShotSpec) bool {
		return slices.Equal(args, spec.Cmd) &&
			spec.Image == demoImage &&
			spec.HostNetwork == hostNetwork &&
			slices.Contains(spec.Env, "RESTIC_REPOSITORY="+demoRepoURL)
	})
}

// respondWithOutput writes stdout and stderr to a run, verbatim.
func respondWithOutput(stdout, stderr string) func(mock.Arguments) {
	return func(args mock.Arguments) {
		spec := args.Get(1).(model.OneShotSpec)
		_, _ = spec.Stdout.Write([]byte(stdout))
		_, _ = spec.Stderr.Write([]byte(stderr))
	}
}

func TestInspector_ReadSnapshots(t *testing.T) {
	tests := []struct {
		name    string
		stdout  string
		runErr  error
		want    []Snapshot
		wantErr string
	}{
		{
			name:   "every snapshot, by the savepoint its tag names",
			stdout: readFixture(t, snapshotsFixture),
			want: []Snapshot{
				{
					ID:        "74b299bb727fee74be6de1a9cb851e773b20f2a4d350db93bb5bd21740b25c91",
					Savepoint: "019e843e-1650-7000-8000-000000000001",
					Time:      time.Date(2026, 10, 24, 3, 58, 45, 822957048, time.UTC),
				},
				{
					ID:        "028a1131099e6748785450129b6d216489c4731b3740ab7fdc4ce4b276174407",
					Savepoint: "019e843e-1650-7000-8000-000000000002",
					Time:      time.Date(2026, 10, 24, 3, 58, 46, 800918799, time.UTC),
				},
				{
					ID:   "367e2c1a2174dd8b100bf1eec54d62037f32284cad25c988b11a4938d6f20040",
					Time: time.Date(2026, 10, 24, 3, 58, 47, 754530466, time.UTC),
				},
			},
		},
		{
			name:   "a repository without snapshots",
			stdout: "[]\n",
			want:   []Snapshot{},
		},
		{
			name:    "output that is not restic's",
			stdout:  "not json",
			wantErr: "decode restic snapshots",
		},
		{
			name:    "a failed run",
			runErr:  &model.ContainerExitError{Image: demoImage, StatusCode: 12},
			wantErr: "exited with status 12",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			containers := &mockRunner{}
			containers.On("RunOneShot", mock.Anything, matchInspection([]string{"snapshots", "--json", "--no-lock"}, true)).
				Run(respondWithOutput(tt.stdout, "")).Return(tt.runErr).Once()
			inspector := NewInspector(containers, demoRepoURLBase, Options{Image: demoImage, HostNetwork: true})

			// Act
			got, err := inspector.ReadSnapshots(zerolog.Nop().WithContext(context.Background()), demoObjectID)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
			containers.AssertExpectations(t)
		})
	}
}

func TestParseSavepointTag(t *testing.T) {
	tests := []struct {
		name string
		tags []string
		want model.SavepointID
	}{
		{
			name: "the savepoint tag among others",
			tags: []string{"arcave-safety-snapshot=true", "arcave-savepoint=019e843e-1650-7000-8000-000000000001"},
			want: "019e843e-1650-7000-8000-000000000001",
		},
		{
			name: "no tags",
		},
		{
			name: "no savepoint tag",
			tags: []string{"nightly"},
		},
		{
			name: "a savepoint tag naming no savepoint id",
			tags: []string{"arcave-savepoint=../config"},
		},
		{
			name: "a tag merely starting like it",
			tags: []string{"arcave-savepoint-old=019e843e-1650-7000-8000-000000000001"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := parseSavepointTag(tt.tags)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestInspector_EstimateForget(t *testing.T) {
	tests := []struct {
		name   string
		ids    []string
		stdout string
		stderr string
		runErr error
		// wantRun is false when the ids are refused before restic runs.
		wantRun bool
		want    *int64
		wantErr string
	}{
		{
			name:    "the total prune line, in KiB as restic rounds it",
			ids:     []string{forgottenSnapshot, otherSnapshot},
			stdout:  readFixture(t, forgetFixture),
			wantRun: true,
			want:    utils.New(int64(301167)),
		},
		{
			name:    "bytes",
			ids:     []string{forgottenSnapshot},
			stdout:  "total prune:           1 blobs / 245 B\n",
			wantRun: true,
			want:    utils.New(int64(245)),
		},
		{
			name:    "MiB",
			ids:     []string{forgottenSnapshot},
			stdout:  "total prune:           9 blobs / 1.500 MiB\n",
			wantRun: true,
			want:    utils.New(int64(1572864)),
		},
		{
			name:    "no estimate when restic found no snapshot to forget",
			ids:     []string{forgottenSnapshot},
			stderr:  `Ignoring "` + forgottenSnapshot + `": failed to load snapshot [null]: no such file or directory` + "\n",
			wantRun: true,
		},
		{
			name:    "a failed run, by restic's own diagnosis",
			ids:     []string{forgottenSnapshot},
			stderr:  "Fatal: wrong password or no key found\n",
			runErr:  &model.ContainerExitError{Image: demoImage, StatusCode: 12},
			wantRun: true,
			wantErr: "wrong password or no key found",
		},
		{
			name:    "an id that is not restic's is no argument",
			ids:     []string{"--no-cache"},
			wantErr: "not a full restic id",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			containers := &mockRunner{}
			if tt.wantRun {
				args := append(append([]string{"forget"}, tt.ids...), "--prune", "--dry-run", "--no-lock")
				containers.On("RunOneShot", mock.Anything, matchInspection(args, false)).
					Run(respondWithOutput(tt.stdout, tt.stderr)).Return(tt.runErr).Once()
			}
			inspector := NewInspector(containers, demoRepoURLBase, Options{Image: demoImage})

			// Act
			got, err := inspector.EstimateForget(zerolog.Nop().WithContext(context.Background()), demoObjectID, tt.ids)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
			containers.AssertExpectations(t)
		})
	}
}

func TestListRepositories(t *testing.T) {
	tests := []struct {
		name    string
		objects []string
		want    []string
	}{
		{
			name: "one repository per directory under restic/",
			objects: []string{
				"restic/volume-named-db/config",
				"restic/volume-named-cache/snapshots/" + forgottenSnapshot,
				"019e843e-1650-7000-8000-000000000001/savepoint.json",
			},
			want: []string{"volume-named-cache", "volume-named-db"},
		},
		{
			name:    "no restic/ at all",
			objects: []string{"019e843e-1650-7000-8000-000000000001/savepoint.json"},
			want:    []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			root := t.TempDir()
			for _, name := range tt.objects {
				full := filepath.Join(root, filepath.FromSlash(name))
				require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
				require.NoError(t, os.WriteFile(full, []byte("x"), 0o644))
			}
			store, err := directory.NewStorage(directory.Config{RootPath: root})
			require.NoError(t, err)

			// Act
			got, err := ListRepositories(context.Background(), store)

			// Assert
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}

// A run without --json ends on a plain Fatal line, and that line fronts its error.
func TestRunner_output_ReportsAPlainFatalLine(t *testing.T) {
	// Arrange
	exit := &model.ContainerExitError{Image: demoImage, StatusCode: 1}
	containers := &mockRunner{}
	containers.On("RunOneShot", mock.Anything, matchRestic("forget")).
		Run(respondWithOutput("", "Fatal: unable to open config file\n")).Return(exit).Once()
	r := runner{containers: containers, image: demoImage, repoURL: demoRepoURL}

	// Act
	_, err := r.output(zerolog.Nop().WithContext(context.Background()), resticCmd{args: []string{"forget"}})

	// Assert
	require.ErrorContains(t, err, "unable to open config file")
	assert.ErrorIs(t, err, exit)
	containers.AssertExpectations(t)
}
