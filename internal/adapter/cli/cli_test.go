package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/gm-tools/internal/config"
	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/service"
)

// capturePrinter records everything a handler prints, so a test can assert on
// the exact value marshalled without inspecting stdout. Its err lets a test
// simulate a printer failure.
type capturePrinter struct {
	got []any
	err error
}

func (p *capturePrinter) Print(v any) error {
	p.got = append(p.got, v)
	return p.err
}

// fakeService satisfies gmService via function fields; unset methods panic, so
// each test wires only the one it exercises.
type fakeService struct {
	createSavepoint  func(context.Context, config.Config, string, string) (model.Savepoint, error)
	listSavepoints   func() ([]model.Savepoint, error)
	removeSavepoints func(config.Config, service.RemoveRequest) (service.RemoveResult, error)
	rollback         func(context.Context, config.Config, string, int) (service.RollbackResult, error)
}

func (f fakeService) Init(context.Context, config.Config, bool) (string, string, error) {
	panic("not wired")
}

func (f fakeService) CreateSavepoint(ctx context.Context, cfg config.Config, name, desc string) (model.Savepoint, error) {
	return f.createSavepoint(ctx, cfg, name, desc)
}

func (f fakeService) ListSavepoints() ([]model.Savepoint, error) {
	return f.listSavepoints()
}

func (f fakeService) RemoveSavepoints(cfg config.Config, req service.RemoveRequest) (service.RemoveResult, error) {
	return f.removeSavepoints(cfg, req)
}

func (f fakeService) Rollback(ctx context.Context, cfg config.Config, ref string, wait int) (service.RollbackResult, error) {
	return f.rollback(ctx, cfg, ref, wait)
}

func TestSavepointCreatePrintsSavepoint(t *testing.T) {
	sp := model.Savepoint{ID: "1780335122", Name: "before-migration"}
	p := &capturePrinter{}
	h := New(fakeService{
		createSavepoint: func(context.Context, config.Config, string, string) (model.Savepoint, error) {
			return sp, nil
		},
	}, p)

	// Act
	err := h.SavepointCreate(context.Background(), config.Config{}, "before-migration", "")

	// Assert
	require.NoError(t, err)
	require.Len(t, p.got, 1)
	assert.Equal(t, sp, p.got[0])
}

func TestRollbackMapsRestoredMounts(t *testing.T) {
	res := service.RollbackResult{
		Savepoint: model.Savepoint{ID: "1780335122"},
		Restored: []model.Mount{
			{Service: "db", Target: "/var/lib/postgresql/data"},
			{Service: "cache", Target: "/data"},
		},
	}
	p := &capturePrinter{}
	h := New(fakeService{
		rollback: func(context.Context, config.Config, string, int) (service.RollbackResult, error) {
			return res, nil
		},
	}, p)

	// Act
	err := h.Rollback(context.Background(), config.Config{}, "latest", DefaultWaitTimeout)

	// Assert
	require.NoError(t, err)
	require.Len(t, p.got, 1)
	assert.Equal(t, model.RollbackResponse{
		RolledBackTo: res.Savepoint,
		Restored: []model.RestoredMount{
			{Service: "db", MountPath: "/var/lib/postgresql/data"},
			{Service: "cache", MountPath: "/data"},
		},
	}, p.got[0])
}

func TestHandlersPropagateServiceAndPrinterErrors(t *testing.T) {
	svcErr := errors.New("boom")
	printErr := errors.New("stdout closed")

	tests := []struct {
		name       string
		listErr    error
		printerErr error
		wantErr    error
		wantPrints int
	}{
		{name: "service error skips printing", listErr: svcErr, wantErr: svcErr, wantPrints: 0},
		{name: "printer error surfaces", printerErr: printErr, wantErr: printErr, wantPrints: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &capturePrinter{err: tt.printerErr}
			h := New(fakeService{
				listSavepoints: func() ([]model.Savepoint, error) {
					return []model.Savepoint{}, tt.listErr
				},
			}, p)

			// Act
			err := h.SavepointList()

			// Assert
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Len(t, p.got, tt.wantPrints)
		})
	}
}
