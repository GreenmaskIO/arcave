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
	cfg := config.Config{Compose: config.Compose{WaitTimeout: waitTimeout}}
	wantLatest := true
	wantReq := model.RollbackRequest{
		Latest:      &wantLatest,
		WaitTimeout: waitTimeout,
	}
	savepoint := model.Savepoint{ID: "019e843e-1650-7000-8000-000000000000"}
	restored := []model.Volume{
		{
			ObjectID: "volume-named-demo_pgdata",
			Type:     model.VolumeTypeNamed,
			Backend:  model.SnapshotBackendFullCopy,
			Named: &model.VolumePropertiesNamed{
				VolumeName: "demo_pgdata",
				UsedBy:     []model.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}},
			},
		},
		{
			ObjectID: "volume-anon-cache-1-data",
			Type:     model.VolumeTypeAnon,
			Backend:  model.SnapshotBackendFullCopy,
			Anon:     &model.VolumePropertiesAnon{Service: "cache", Replica: 1, Target: "/data"},
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
				Savepoint: savepoint,
				Restored:  restored,
			},
			svcErr:     nil,
			printerErr: nil,
			wantErr:    nil,
			wantPrinted: []any{model.RollbackResponse{
				RolledBackTo: savepoint,
				Restored:     restored,
			}},
		},
		{
			name:       "restores nothing",
			res:        model.RollbackResult{Savepoint: savepoint, Restored: []model.Volume{}},
			svcErr:     nil,
			printerErr: nil,
			wantErr:    nil,
			wantPrinted: []any{model.RollbackResponse{
				RolledBackTo: savepoint,
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
			res:        model.RollbackResult{Savepoint: savepoint, Restored: []model.Volume{}},
			svcErr:     nil,
			printerErr: printErr,
			wantErr:    printErr,
			wantPrinted: []any{model.RollbackResponse{
				RolledBackTo: savepoint,
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

// An id must never be read as a name, or `rollback <id>` would resolve to a
// savepoint that merely calls itself that.
func TestNewRollbackRequest(t *testing.T) {
	tests := []struct {
		name       string
		ref        string
		wantID     *model.SavepointID
		wantName   *string
		wantLatest *bool
		wantErr    error
	}{
		{name: "the reserved word", ref: "latest", wantLatest: ptr(true)},
		{name: "a canonical UUID is an id", ref: "019e843e-1650-7000-8000-000000000000", wantID: ptr(model.SavepointID("019e843e-1650-7000-8000-000000000000"))},
		{name: "anything else is a name", ref: "before-migration", wantName: ptr("before-migration")},
		// SavepointID.Validate admits the canonical spelling alone, so a suffix,
		// upper case or a run of digits can only be a name.
		{name: "a UUID with a suffix is a name", ref: "019e843e-1650-7000-8000-000000000000-b", wantName: ptr("019e843e-1650-7000-8000-000000000000-b")},
		{name: "an upper-case UUID is a name", ref: "019E843E-1650-7000-8000-000000000000", wantName: ptr("019E843E-1650-7000-8000-000000000000")},
		{name: "a run of digits is a name", ref: "1780335122", wantName: ptr("1780335122")},
		// The empty token needs no case of its own: it lands on the name branch and
		// Validate refuses it, which is what keeps `arcave rollback ""` from
		// resolving to anything.
		{name: "the empty token selects nothing", ref: "", wantName: ptr(""), wantErr: model.ErrRollbackNoSelector},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newRollbackRequest(tt.ref, 90*time.Second, model.LeftoverVolumesRefuse)

			// Assert
			assert.Equal(t, tt.wantID, got.ID)
			assert.Equal(t, tt.wantName, got.Name)
			assert.Equal(t, tt.wantLatest, got.Latest)
			assert.Equal(t, 90*time.Second, got.WaitTimeout)
			assert.Equal(t, model.LeftoverVolumesRefuse, got.LeftoverVolumes)
			assert.ErrorIs(t, got.Validate(), tt.wantErr)
			// Whatever it chose renders back as the word that was typed, which is what
			// error messages quote and what a safety snapshot's name carries.
			assert.Equal(t, tt.ref, got.Ref())
		})
	}
}

// ptr renders a literal as one of RollbackRequest's optional selectors.
func ptr[T any](v T) *T { return &v }
