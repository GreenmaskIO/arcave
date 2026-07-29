package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
)

// waitTimeout is an arbitrary configured bound. The handler carries
// compose.wait_timeout into the request untouched — bar the unit conversion the
// request's plain time.Duration asks for — so the value means nothing beyond
// being distinct from the package default a mistake would fall back to.
const waitTimeout = 90 * time.Second

func TestRollbackHandler_Rollback(t *testing.T) {
	cfg := config.Config{
		Project: config.Project{ComposeFiles: []string{"docker-compose.yaml"}},
		Compose: config.Compose{WaitTimeout: waitTimeout},
	}
	wantReq := model.RollbackRequest{
		ComposeFiles: []string{"docker-compose.yaml"},
		Ref:          "latest",
		WaitTimeout:  waitTimeout,
	}
	sp := model.Savepoint{ID: "1780335122"}
	restored := []model.Volume{
		{
			ObjectID: "volume/demo_pgdata",
			Type:     model.VolumeTypeNamed,
			Backend:  model.SnapshotBackendCopy,
			Named: &model.VolumePropertiesNamed{
				VolumeName: "demo_pgdata",
				UsedBy:     []model.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}},
			},
		},
		{
			ObjectID: "service/cache/data",
			Type:     model.VolumeTypeAnon,
			Backend:  model.SnapshotBackendCopy,
			Anon:     &model.VolumePropertiesAnon{Service: "cache", Target: "/data"},
		},
	}
	svcErr := errors.New("savepoint not found")
	printErr := errors.New("stdout closed")

	tests := []struct {
		name        string
		res         model.RollbackResult // what the service returns
		svcErr      error                // the service's error, if any
		printerErr  error                // the printer's error, if any
		wantErr     error                // what Rollback must return
		wantPrinted []any                // every value handed to the printer, in order
	}{
		{
			// The handler maps nothing: what a savepoint covers is already the
			// domain's own type. The "empty slice, never nil" guarantee is the
			// service's now, not this adapter's.
			name: "forwards every restored volume",
			res: model.RollbackResult{
				Savepoint: sp,
				Restored:  restored,
			},
			svcErr:     nil,
			printerErr: nil,
			wantErr:    nil,
			wantPrinted: []any{model.RollbackResponse{
				RolledBackTo: sp,
				Restored:     restored,
			}},
		},
		{
			name:       "restores nothing",
			res:        model.RollbackResult{Savepoint: sp, Restored: []model.Volume{}},
			svcErr:     nil,
			printerErr: nil,
			wantErr:    nil,
			wantPrinted: []any{model.RollbackResponse{
				RolledBackTo: sp,
				Restored:     []model.Volume{},
			}},
		},
		{
			name:        "service error skips printing",
			res:         model.RollbackResult{},
			svcErr:      svcErr,
			printerErr:  nil,
			wantErr:     svcErr,
			wantPrinted: nil,
		},
		{
			name:       "printer error surfaces",
			res:        model.RollbackResult{Savepoint: sp, Restored: []model.Volume{}},
			svcErr:     nil,
			printerErr: printErr,
			wantErr:    printErr,
			wantPrinted: []any{model.RollbackResponse{
				RolledBackTo: sp,
				Restored:     []model.Volume{},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			p := &capturePrinter{}
			p.On("Print", mock.Anything).Return(tt.printerErr).Maybe()
			svc := &mockService{}
			svc.On("Rollback", mock.Anything, wantReq).Return(tt.res, tt.svcErr)
			h := NewRollbackHandler(svc, p)

			// Act
			err := h.Rollback(context.Background(), cfg, "latest")

			// Assert
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.wantPrinted, p.printed())
			svc.AssertExpectations(t)
		})
	}
}
