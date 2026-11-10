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

package sessionref

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

var (
	exp = model.Session{
		ID:        mustSessionID("019e0000-0000-7000-8000-000000000001"),
		Name:      "exp",
		CreatedAt: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC),
	}
	gone       = mustSessionID("019e0000-0000-7000-8000-000000000002")
	errPointer = errors.New("decoding the current session")
)

// listed is the arcave project's sessions every resolve reads.
var listed = Listed{model.DefaultSessionRecord, exp}

// mockCurrent is a strict pointer reader: a test expecting no read sets no expectation.
type mockCurrent struct{ mock.Mock }

func (m *mockCurrent) Get(ctx context.Context) (model.SessionID, bool, error) {
	args := m.Called(ctx)
	return args.Get(0).(model.SessionID), args.Bool(1), args.Error(2)
}

// mockLister is a strict session lister: a test expecting no listing sets no expectation.
type mockLister struct{ mock.Mock }

func (m *mockLister) ListSessions(ctx context.Context) ([]model.Session, error) {
	args := m.Called(ctx)
	return args.Get(0).([]model.Session), args.Error(1)
}

type fakeCurrent struct {
	id    model.SessionID
	found bool
	err   error
}

func (f fakeCurrent) Get(context.Context) (model.SessionID, bool, error) { return f.id, f.found, f.err }

func TestResolve(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		current fakeCurrent
		want    model.Session
		wantErr error
	}{
		{name: "no ref and no pointer is the default", want: model.DefaultSessionRecord},
		{name: "a name", ref: "exp", want: exp},
		{name: "an id", ref: exp.ID.String(), want: exp},
		{name: "an unknown ref", ref: "nope", wantErr: model.ErrSessionNotFound},
		{name: "a ref outranks a dangling pointer", ref: "baseline", current: fakeCurrent{id: gone, found: true},
			want: model.DefaultSessionRecord},
		{name: "a pointer to a session", current: fakeCurrent{id: exp.ID, found: true}, want: exp},
		{name: "a pointer to a deleted session is refused", current: fakeCurrent{id: gone, found: true},
			wantErr: model.ErrCurrentSessionNotFound},
		{name: "an unreadable pointer is refused", current: fakeCurrent{err: errPointer}, wantErr: errPointer},
		{name: "current is refused", ref: model.RefCurrent, wantErr: model.ErrSessionRefKeyword},
		{name: "latest is refused", ref: model.RefLatest, wantErr: model.ErrSessionRefKeyword},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := Resolve(context.Background(), listed, tt.current, tt.ref)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// The default named by no ref and no pointer reads no session record.
func TestResolve_DefaultListsNothing(t *testing.T) {
	// Arrange
	sessions := &mockLister{}

	// Act
	got, err := Resolve(context.Background(), sessions, fakeCurrent{}, "")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.DefaultSessionRecord, got)
	sessions.AssertExpectations(t)
}

func TestResolve_ListingFails(t *testing.T) {
	// Arrange
	errList := errors.New("reading session.json")
	sessions := &mockLister{}
	sessions.On("ListSessions", mock.Anything).Return([]model.Session(nil), errList).Once()

	// Act
	_, err := Resolve(context.Background(), sessions, fakeCurrent{}, "exp")

	// Assert
	require.ErrorIs(t, err, errList)
	sessions.AssertExpectations(t)
}

func TestResolveCurrent_Source(t *testing.T) {
	baseline := &model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName}
	tests := []struct {
		name    string
		ref     model.CurrentSessionRef
		current fakeCurrent
		want    model.CurrentSessionSource
	}{
		{name: "no ref and no pointer", want: model.CurrentSessionSource{Source: model.SessionRefSourceDefault}},
		{name: "the pointer", current: fakeCurrent{id: exp.ID, found: true},
			want: model.CurrentSessionSource{Source: model.SessionRefSourcePointer}},
		{name: "the variable over no pointer overrides the default",
			ref:  model.CurrentSessionRef{Ref: "exp", Source: model.SessionRefSourceEnv},
			want: model.CurrentSessionSource{Source: model.SessionRefSourceEnv, Overrides: baseline}},
		{name: "the flag over the pointer overrides it",
			ref:     model.CurrentSessionRef{Ref: model.DefaultSessionName, Source: model.SessionRefSourceFlag},
			current: fakeCurrent{id: exp.ID, found: true},
			want: model.CurrentSessionSource{Source: model.SessionRefSourceFlag,
				Overrides: &model.SessionRef{ID: exp.ID, Name: exp.Name}}},
		{name: "a flag naming what the pointer names overrides nothing",
			ref:     model.CurrentSessionRef{Ref: "exp", Source: model.SessionRefSourceFlag},
			current: fakeCurrent{id: exp.ID, found: true},
			want:    model.CurrentSessionSource{Source: model.SessionRefSourceFlag}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			_, got, err := ResolveCurrent(context.Background(), listed, tt.current, tt.ref)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A ref no flag or variable gave — a positional one, status's — reads no pointer and overrides nothing.
func TestResolveCurrent_RefWithoutSourceReadsNoPointer(t *testing.T) {
	// Arrange
	current := &mockCurrent{}

	// Act
	session, source, err := ResolveCurrent(context.Background(), listed, current, model.CurrentSessionRef{Ref: "exp"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, exp, session)
	assert.Nil(t, source.Overrides)
	current.AssertExpectations(t)
}

func TestFindOverridden(t *testing.T) {
	tests := []struct {
		name    string
		current fakeCurrent
		chosen  model.SessionID
		want    *model.SessionRef
	}{
		{name: "no pointer: the default", chosen: exp.ID,
			want: &model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName}},
		{name: "a pointer to another session", current: fakeCurrent{id: exp.ID, found: true},
			chosen: model.DefaultSession, want: &model.SessionRef{ID: exp.ID, Name: exp.Name}},
		{name: "a pointer to the chosen session", current: fakeCurrent{id: exp.ID, found: true}, chosen: exp.ID},
		{name: "no pointer, the default chosen", chosen: model.DefaultSession},
		{name: "a dangling pointer", current: fakeCurrent{id: gone, found: true}, chosen: exp.ID},
		{name: "an unreadable pointer", current: fakeCurrent{err: errPointer}, chosen: exp.ID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := FindOverridden(context.Background(), listed, tt.current, tt.chosen)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewCurrentNotFoundError(t *testing.T) {
	// Act
	err := NewCurrentNotFoundError(gone)

	// Assert
	require.ErrorIs(t, err, model.ErrCurrentSessionNotFound)
	assert.Equal(t, "the current session no longer exists: "+gone.String()+", named by "+model.StateDir+
		"/current_session", err.Error())
}

func TestReadCurrent_Unreadable(t *testing.T) {
	// Arrange
	unreadable := fmt.Errorf("%w: decoding current_session: %w", model.ErrCurrentSessionUnreadable, errPointer)

	// Act
	_, _, err := ReadCurrent(context.Background(), fakeCurrent{err: unreadable})

	// Assert
	require.ErrorIs(t, err, model.ErrCurrentSessionUnreadable)
	require.ErrorIs(t, err, errPointer)
	assert.NotContains(t, err.Error(), "arcave session")
}

func mustSessionID(s string) model.SessionID {
	id, err := model.ParseSessionID(s)
	if err != nil {
		panic(err)
	}
	return id
}
