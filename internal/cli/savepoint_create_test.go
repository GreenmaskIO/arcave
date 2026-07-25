package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
)

func TestSavepointCreateHandler_SavepointCreate(t *testing.T) {
	cfg := config.Config{
		Project: config.Project{ComposeFiles: []string{"docker-compose.yaml"}},
		Compose: config.Compose{WaitTimeout: waitTimeout},
	}
	wantReq := model.CreateSavepointRequest{
		ComposeFiles: []string{"docker-compose.yaml"},
		Name:         "before-migration",
		Description:  "schema v2",
		WaitTimeout:  waitTimeout,
	}
	sp := model.Savepoint{ID: "1780335122", Name: "before-migration", Description: "schema v2"}
	svcErr := errors.New("services did not become healthy")
	printErr := errors.New("stdout closed")

	tests := []struct {
		name        string
		sp          model.Savepoint // what the service returns
		svcErr      error           // the service's error, if any
		printerErr  error           // the printer's error, if any
		wantErr     error           // what SavepointCreate must return
		wantPrinted []any           // every value handed to the printer, in order
	}{
		{
			name:        "prints the created savepoint",
			sp:          sp,
			svcErr:      nil,
			printerErr:  nil,
			wantErr:     nil,
			wantPrinted: []any{sp},
		},
		{
			name:        "service error skips printing",
			sp:          model.Savepoint{},
			svcErr:      svcErr,
			printerErr:  nil,
			wantErr:     svcErr,
			wantPrinted: nil,
		},
		{
			name:        "printer error surfaces",
			sp:          sp,
			svcErr:      nil,
			printerErr:  printErr,
			wantErr:     printErr,
			wantPrinted: []any{sp},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			p := &capturePrinter{}
			p.On("Print", mock.Anything).Return(tt.printerErr).Maybe()
			svc := &mockService{}
			svc.On("CreateSavepoint", mock.Anything, wantReq).Return(tt.sp, tt.svcErr)
			h := NewSavepointCreateHandler(svc, p)

			// Act
			err := h.SavepointCreate(context.Background(), cfg, "before-migration", "schema v2")

			// Assert
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.wantPrinted, p.printed())
			svc.AssertExpectations(t)
			p.AssertExpectations(t)
		})
	}
}
