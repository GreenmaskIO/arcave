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
	"testing"

	"github.com/greenmaskio/storages/directory"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			name:    "a directory that is no volume's is left out",
			objects: []string{"restic/volume-named-db/config", "restic/locks/a1"},
			want:    []string{"volume-named-db"},
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
			got, err := ListRepositories(zerolog.Nop().WithContext(context.Background()), store)

			// Assert
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}

func TestParseRepositoryKey(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		want    string
		wantErr bool
	}{
		{name: "a key NewRepositoryKey built", key: NewRepositoryKey(demoObjectID), want: demoObjectID},
		{name: "another namespace", key: "shared/" + demoObjectID, wantErr: true},
		{name: "a snapshot key", key: NewSnapshotKey(demoObjectID, forgottenSnapshot), wantErr: true},
		{name: "the repositories' root itself", key: "restic/", wantErr: true},
		{name: "a repository climbing out", key: "restic/..", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := ParseRepositoryKey(tt.key)

			// Assert
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseSnapshotKey(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		wantObjectID string
		wantID       string
		wantErr      bool
	}{
		{
			name:         "a key NewSnapshotKey built",
			key:          NewSnapshotKey(demoObjectID, forgottenSnapshot),
			wantObjectID: demoObjectID,
			wantID:       forgottenSnapshot,
		},
		{name: "another namespace", key: "shared/" + demoObjectID + "/snapshots/" + forgottenSnapshot, wantErr: true},
		{name: "not under snapshots/", key: "restic/" + demoObjectID + "/locks/" + forgottenSnapshot, wantErr: true},
		{name: "a segment too many", key: NewSnapshotKey(demoObjectID, forgottenSnapshot) + "/x", wantErr: true},
		{name: "a repository climbing out", key: "restic/../snapshots/" + forgottenSnapshot, wantErr: true},
		{name: "no repository", key: "restic//snapshots/" + forgottenSnapshot, wantErr: true},
		{name: "the repositories' root itself", key: "restic/./snapshots/" + forgottenSnapshot, wantErr: true},
		{name: "a repository that is no volume's", key: "restic/locks/snapshots/" + forgottenSnapshot, wantErr: true},
		{
			name:         "a mount target flattened with a space",
			key:          NewSnapshotKey("volume-anon-app-1-var-my data", forgottenSnapshot),
			wantObjectID: "volume-anon-app-1-var-my data",
			wantID:       forgottenSnapshot,
		},
		{name: "a short id", key: NewSnapshotKey(demoObjectID, "3f8a"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			objectID, id, err := ParseSnapshotKey(tt.key)

			// Assert
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantObjectID, objectID)
			assert.Equal(t, tt.wantID, id)
		})
	}
}

// A runner is bound to a volume's repository over the transport it is handed.
func TestRepositories_newRunner(t *testing.T) {
	stdio := StdioTransport{Server: &mockServer{}}

	tests := []struct {
		name      string
		transport Transport
		password  string
		objectID  string
		want      runner
		wantErr   string
	}{
		{
			name:      "tcp: a volume's repository",
			transport: demoTCPTransport,
			objectID:  demoObjectID,
			want:      runner{image: demoImage, password: defaultPassword, objectID: demoObjectID, transport: demoTCPTransport},
		},
		{
			name:      "an encrypted store's own password",
			transport: demoTCPTransport,
			password:  demoPassword,
			objectID:  demoObjectID,
			want:      runner{image: demoImage, password: demoPassword, objectID: demoObjectID, transport: demoTCPTransport},
		},
		{name: "tcp: a segment that is no volume's", transport: demoTCPTransport, objectID: "..", wantErr: "no volume's repository"},
		{name: "tcp: a segment holding a path", transport: demoTCPTransport, objectID: "volume-named-db/config", wantErr: "no volume's repository"},
		{
			name:      "stdio: a volume's repository",
			transport: stdio,
			objectID:  demoObjectID,
			want:      runner{image: demoImage, password: defaultPassword, objectID: demoObjectID, transport: stdio},
		},
		{name: "stdio: a segment that is no volume's", transport: stdio, objectID: "..", wantErr: "no volume's repository"},
		{name: "stdio: a segment holding a path", transport: stdio, objectID: "volume-named-db/config", wantErr: "no volume's repository"},
		{name: "no transport", objectID: demoObjectID, wantErr: errNoTransport.Error()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			containers := &mockRunner{}
			options := Options{Image: demoImage, Password: tt.password}

			// Act
			got, err := newRepositories(containers, tt.transport, options).newRunner(tt.objectID)

			// Assert
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			want := tt.want
			want.containers = containers
			assert.Equal(t, want, got)
		})
	}
}
