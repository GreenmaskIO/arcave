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

package core

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
)

const configPath = "/proj/.arcave/config.yaml"

func TestInitService_Init(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.Config
		override bool
		exists   bool // does .arcave/config.yaml already exist?
		wantErr  string
		verify   func(t *testing.T, saved config.Config)
	}{
		{
			name: "writes the config",
			cfg:  config.Config{Project: config.Project{Name: "demo"}},
		},
		{
			name:    "an initialized project is an error",
			cfg:     config.Config{Project: config.Project{Name: "demo"}},
			exists:  true,
			wantErr: "project already initialized",
		},
		{
			name:     "override overwrites an initialized project",
			cfg:      config.Config{Project: config.Project{Name: "demo"}},
			override: true,
			exists:   true,
		},
		{
			// Init persists the config verbatim, so a section it has no flag for
			// survives an --override.
			name: "override preserves the sections init has no flag for",
			cfg: config.Config{
				Project:  config.Project{Name: "demo"},
				Docker:   config.Docker{Host: "tcp://remote:2375"},
				Snapshot: config.Snapshot{Backend: config.SnapshotBackendFullCopy},
			},
			override: true,
			exists:   true,
			verify: func(t *testing.T, saved config.Config) {
				assert.Equal(t, "tcp://remote:2375", saved.Docker.Host)
				assert.Equal(t, config.SnapshotBackendFullCopy, saved.Snapshot.Backend)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			cw := &mockConfig{}
			cw.On("Exists").Return(tt.exists, nil)
			cw.On("Path").Return(configPath).Maybe()
			var saved config.Config
			cw.On("Save", mock.MatchedBy(func(c config.Config) bool {
				return c.Project.Name != ""
			})).Run(func(args mock.Arguments) {
				saved = args.Get(0).(config.Config)
			}).Return(nil).Maybe()
			svc := NewInitService("/proj", cw)

			// Act
			name, path, err := svc.Init(context.Background(), tt.cfg, tt.override)

			// Assert
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				cw.AssertNotCalled(t, "Save", mock.Anything)
				cw.AssertExpectations(t)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "demo", name)
			assert.Equal(t, configPath, path)
			// ApplyDefaults fills the savepoint dir; the project is a segment of the
			// store root, not of the configured path.
			assert.Equal(t, filepath.Join(".arcave", "storage"), saved.Storage.DirectoryPath())
			if tt.verify != nil {
				tt.verify(t, saved)
			}
			cw.AssertExpectations(t)
		})
	}
}

// A Config reaches Init by value, so the defaults it stamps must not reach back
// through the caller's storage-arm pointer.
func TestInitService_Init_LeavesTheCallersConfigUntouched(t *testing.T) {
	// Arrange
	cw := &mockConfig{}
	cw.On("Exists").Return(false, nil)
	cw.On("Path").Return(configPath)
	cw.On("Save", mock.MatchedBy(func(c config.Config) bool {
		return c.Project.Name != ""
	})).Return(nil)
	svc := NewInitService("/proj", cw)
	cfg := config.Config{Storage: config.Storage{Directory: &config.DirectoryStorage{}}}

	// Act
	_, _, err := svc.Init(context.Background(), cfg, false)

	// Assert
	require.NoError(t, err)
	assert.Empty(t, cfg.Project.Name)
	assert.Empty(t, cfg.Storage.DirectoryPath())
	cw.AssertExpectations(t)
}

// The project name defaults to the project root's base name, normalized into
// the charset a store-root segment may carry.
func TestInitService_Init_DefaultsTheName(t *testing.T) {
	// Arrange
	cw := &mockConfig{}
	cw.On("Exists").Return(false, nil)
	cw.On("Path").Return(configPath)
	var saved config.Config
	cw.On("Save", mock.MatchedBy(func(c config.Config) bool {
		return c.Project.Name != ""
	})).Run(func(args mock.Arguments) {
		saved = args.Get(0).(config.Config)
	}).Return(nil)
	svc := NewInitService(filepath.Join("/home", "vadim", "MyProj"), cw)

	// Act
	name, _, err := svc.Init(context.Background(), config.Config{}, false)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "myproj", name)
	assert.Equal(t, "myproj", saved.Project.Name)
	assert.Equal(t, filepath.Join(".arcave", "storage"), saved.Storage.DirectoryPath())
	cw.AssertExpectations(t)
}

// mockConfig doubles configWriter; the seam takes no ctx.
type mockConfig struct{ mock.Mock }

func (m *mockConfig) Exists() (bool, error) {
	args := m.Called()
	return args.Bool(0), args.Error(1)
}

func (m *mockConfig) Save(cfg config.Config) error { return m.Called(cfg).Error(0) }

func (m *mockConfig) Path() string { return m.Called().String(0) }
