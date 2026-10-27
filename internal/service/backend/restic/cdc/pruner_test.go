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
	"cmp"
	"context"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
)

// pruneFixture is the stdout of a real forget <two ids> --prune of restic 0.19.1: its total line
// reads as the dry run's does.
const pruneFixture = "testdata/forget-prune.txt"

func TestPruner_Forget(t *testing.T) {
	tests := []struct {
		name string
		// objectID is demoObjectID when empty.
		objectID string
		ids      []string
		stdout   string
		stderr   string
		runErr   error
		// wantRun is false when the ids are refused before anything is touched.
		wantRun bool
		// wantArgs is forget of ids, then --prune, when nil.
		wantArgs []string
		want     *int64
		wantErr  string
	}{
		{
			name:    "the total line of a real prune",
			ids:     []string{forgottenSnapshot, otherSnapshot},
			stdout:  readFixture(t, pruneFixture),
			wantRun: true,
			want:    utils.New(int64(301032)),
		},
		{
			name:    "no total when restic printed none",
			ids:     []string{forgottenSnapshot},
			stdout:  "done\n",
			wantRun: true,
		},
		{
			name:     "no id only prunes",
			stdout:   "total prune:           3 blobs / 2.000 KiB\n",
			wantRun:  true,
			wantArgs: []string{"prune"},
			want:     utils.New(int64(2048)),
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
		{
			name:     "a repository that is no volume's is no path",
			objectID: "..",
			ids:      []string{forgottenSnapshot},
			wantErr:  "no volume's repository",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			objectID := cmp.Or(tt.objectID, demoObjectID)
			ctx := zerolog.Nop().WithContext(context.Background())
			root := newStore(t)
			repo, err := root.SubStorage(demoObjectID, true)
			require.NoError(t, err)
			require.NoError(t, repo.PutObject(ctx, "locks/a1b2", strings.NewReader("orphan")))
			containers := &mockRunner{}
			if tt.wantRun {
				args := tt.wantArgs
				if args == nil {
					args = append(append([]string{"forget"}, tt.ids...), "--prune")
				}
				containers.On("RunOneShot", mock.Anything, matchInspection(args, false)).
					Run(func(args mock.Arguments) {
						assert.Empty(t, objectsUnder(t, repo, locksPrefix), "the locks go before restic runs")
						respondWithOutput(tt.stdout, tt.stderr)(args)
					}).
					Return(tt.runErr).Once()
			}
			pruner := NewPruner(containers, root, demoRepoURLBase, Options{Image: demoImage})

			// Act
			got, err := pruner.Forget(ctx, objectID, tt.ids)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
			if !tt.wantRun {
				assert.Equal(t, []string{"a1b2"}, objectsUnder(t, repo, locksPrefix), "a refused call touches nothing")
			}
			containers.AssertExpectations(t)
		})
	}
}
