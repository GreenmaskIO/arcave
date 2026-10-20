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
	"github.com/google/uuid"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
)

var handlerSession = model.SessionID(uuid.MustParse("0f9c2f4a-6d3e-4b6f-9a2c-1d5e8b7a4c31"))

func TestSessionListHandler_SessionList(t *testing.T) {
	sessions := []model.SessionSummary{{Session: model.Session{ID: model.DefaultSession}}}
	svcErr := errors.New("store unreadable")
	tests := []struct {
		name        string
		sessions    []model.SessionSummary
		listErr     error
		wantPrinted []any
	}{
		{name: "prints every session", sessions: sessions, wantPrinted: []any{sessions}},
		{name: "service error skips printing", listErr: svcErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			p := &capturePrinter{}
			p.On("Print", mock.Anything).Return(nil).Maybe()
			svc := &mockService{}
			req := model.ListSessionsRequest{All: true}
			svc.On("ListSessions", mock.Anything, req).Return(tt.sessions, tt.listErr).Once()

			// Act
			err := NewSessionListHandler(svc, p).SessionList(context.Background(), req)

			// Assert
			assert.ErrorIs(t, err, tt.listErr)
			assert.Equal(t, tt.wantPrinted, p.printed())
			svc.AssertExpectations(t)
		})
	}
}

func TestSessionCreateHandler_SessionCreate(t *testing.T) {
	// Arrange
	req := model.CreateSessionRequest{Name: "exp", From: "baseline"}
	record := model.Session{ID: handlerSession, Name: "exp", From: &model.SessionOrigin{Session: model.DefaultSession}}
	p := &capturePrinter{}
	p.On("Print", mock.Anything).Return(nil).Once()
	svc := &mockService{}
	svc.On("CreateSession", mock.Anything, req).Return(record, nil).Once()

	// Act
	err := NewSessionCreateHandler(svc, p).SessionCreate(context.Background(), req)

	// Assert
	assert.NoError(t, err)
	assert.Equal(t, []any{record}, p.printed())
	svc.AssertExpectations(t)
}

func TestSessionRemoveHandler_SessionRemove(t *testing.T) {
	refused := model.ErrDefaultSessionDelete
	tests := []struct {
		name        string
		req         model.RemoveSessionRequest
		res         model.RemoveSessionResponse
		deleteErr   error
		wantPrinted []any
	}{
		{name: "prints the response as is", req: model.RemoveSessionRequest{Session: "exp", Force: true},
			res:         model.RemoveSessionResponse{Session: handlerSession, Removed: true, FreedBytes: 4096},
			wantPrinted: []any{model.RemoveSessionResponse{Session: handlerSession, Removed: true, FreedBytes: 4096}}},
		{name: "a refusal skips printing", req: model.RemoveSessionRequest{Session: "baseline"}, deleteErr: refused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			p := &capturePrinter{}
			p.On("Print", mock.Anything).Return(nil).Maybe()
			svc := &mockService{}
			svc.On("RemoveSession", mock.Anything, tt.req).Return(tt.res, tt.deleteErr).Once()

			// Act
			err := NewSessionRemoveHandler(svc, p).SessionRemove(context.Background(), tt.req)

			// Assert
			assert.ErrorIs(t, err, tt.deleteErr)
			assert.Equal(t, tt.wantPrinted, p.printed())
			svc.AssertExpectations(t)
		})
	}
}

func TestSessionSetHandler_SessionSet(t *testing.T) {
	// Arrange
	record := model.Session{ID: handlerSession, Name: "exp"}
	p := &capturePrinter{}
	p.On("Print", mock.Anything).Return(nil).Once()
	svc := &mockService{}
	svc.On("SetSession", mock.Anything, "exp").Return(record, nil).Once()

	// Act
	err := NewSessionSetHandler(svc, p).SessionSet(context.Background(), "exp")

	// Assert
	assert.NoError(t, err)
	assert.Equal(t, []any{record}, p.printed())
	svc.AssertExpectations(t)
}

func TestSessionUnsetHandler_SessionUnset(t *testing.T) {
	// Arrange
	p := &capturePrinter{}
	p.On("Print", mock.Anything).Return(nil).Once()
	svc := &mockService{}
	svc.On("UnsetSession", mock.Anything).Return(model.DefaultSessionRecord, nil).Once()

	// Act
	err := NewSessionUnsetHandler(svc, p).SessionUnset(context.Background())

	// Assert
	assert.NoError(t, err)
	assert.Equal(t, []any{model.DefaultSessionRecord}, p.printed())
	svc.AssertExpectations(t)
}

func TestSessionCurrentHandler_SessionCurrent(t *testing.T) {
	// Arrange
	record := model.Session{ID: handlerSession, Name: "exp"}
	p := &capturePrinter{}
	p.On("Print", mock.Anything).Return(nil).Once()
	svc := &mockService{}
	svc.On("ResolveSession", mock.Anything, "").Return(record, nil).Once()

	// Act
	err := NewSessionCurrentHandler(svc, p).SessionCurrent(context.Background(), "")

	// Assert
	assert.NoError(t, err)
	assert.Equal(t, []any{record}, p.printed())
	svc.AssertExpectations(t)
}
