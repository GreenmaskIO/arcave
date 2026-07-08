package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/gm-tools/internal/config"
)

// Init reports through the context logger, never the printer, so the contract
// under test is that the resolved config reaches the service verbatim — every
// section of it, including the ones init has no flag for (docker, snapshot) —
// plus the error passthrough. Every case therefore expects an empty printer.
func TestConfigInitHandler_Init(t *testing.T) {
	cfg := config.Config{
		Version: config.Version,
		Project: config.Project{
			Name:         "demo",
			ComposeFiles: []string{"docker-compose.yaml", "docker-compose.override.yaml"},
		},
		Docker: config.Docker{Host: "tcp://remote:2375"},
		Storage: config.Storage{
			Type:      config.StorageTypeDirectory,
			Directory: &config.DirectoryStorage{Path: ".gm/savepoints/demo"},
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
			svc.On("Init", mock.Anything, cfg, true).Return("demo", ".gm/config.yaml", tt.svcErr)
			h := NewConfigInitHandler(svc, p)

			// Act
			err := h.Init(context.Background(), cfg, true)

			// Assert
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Empty(t, p.got, "Init must never print")
			svc.AssertExpectations(t)
		})
	}
}
