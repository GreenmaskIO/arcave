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

package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/config"
)

// Init reports through the context logger, never the printer, so the contract
// under test is that the resolved config reaches the service verbatim — every
// section of it, including the ones init has no flag for (docker, snapshot) —
// plus the error passthrough. Every case therefore expects an empty printer.
func TestConfigInitHandler_Init(t *testing.T) {
	cfg := config.Config{
		Project: config.Project{Name: "demo"},
		Docker:  config.Docker{Host: "tcp://remote:2375"},
		Storage: config.Storage{
			Type:      config.StorageTypeDirectory,
			Directory: &config.DirectoryStorage{Path: ".arcave/savepoints/demo"},
		},
		Snapshot: config.Snapshot{Backend: config.SnapshotBackendFullCopy},
		Log:      config.Log{Level: "debug", Format: "console"},
	}
	svcErr := errors.New("project already initialized")

	tests := []struct {
		name    string
		svcErr  error // what the service returns
		wantErr error // what Init must return
	}{
		{
			name:    "initializes the project",
			svcErr:  nil,
			wantErr: nil,
		},
		{
			name:    "service error surfaces",
			svcErr:  svcErr,
			wantErr: svcErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			p := &capturePrinter{}
			svc := &mockService{}
			svc.On("Init", mock.Anything, cfg, true).Return("demo", ".arcave/config.yaml", tt.svcErr)
			h := NewConfigInitHandler(svc, p)

			// Act
			err := h.Init(context.Background(), cfg, true)

			// Assert
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Empty(t, p.printed(), "Init must never print")
			svc.AssertExpectations(t)
		})
	}
}
