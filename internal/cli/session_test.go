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
			svc.On("ListSessions", mock.Anything).Return(tt.sessions, tt.listErr).Once()

			// Act
			err := NewSessionListHandler(svc, p).SessionList(context.Background())

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

func TestSessionDeleteHandler_SessionDelete(t *testing.T) {
	refused := model.ErrDefaultSessionDelete
	tests := []struct {
		name        string
		ref         string
		resp        model.DeleteSessionResponse
		deleteErr   error
		wantPrinted []any
	}{
		{name: "prints the response as is", ref: "exp",
			resp:        model.DeleteSessionResponse{Session: handlerSession, Deleted: true, FreedBytes: 4096},
			wantPrinted: []any{model.DeleteSessionResponse{Session: handlerSession, Deleted: true, FreedBytes: 4096}}},
		{name: "a refusal skips printing", ref: "baseline", deleteErr: refused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			p := &capturePrinter{}
			p.On("Print", mock.Anything).Return(nil).Maybe()
			svc := &mockService{}
			svc.On("DeleteSession", mock.Anything, tt.ref).Return(tt.resp, tt.deleteErr).Once()

			// Act
			err := NewSessionDeleteHandler(svc, p).SessionDelete(context.Background(), tt.ref)

			// Assert
			assert.ErrorIs(t, err, tt.deleteErr)
			assert.Equal(t, tt.wantPrinted, p.printed())
			svc.AssertExpectations(t)
		})
	}
}
