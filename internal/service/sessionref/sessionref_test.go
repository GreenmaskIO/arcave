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
		list    bool // whether ListSessions is expected
		want    model.Session
		wantErr error
	}{
		{name: "no ref and no pointer is the default", want: model.DefaultSessionRecord},
		{name: "a name", ref: "exp", list: true, want: exp},
		{name: "an id", ref: exp.ID.String(), list: true, want: exp},
		{name: "an unknown ref", ref: "nope", list: true, wantErr: model.ErrSessionNotFound},
		{name: "a ref outranks a dangling pointer", ref: "baseline", current: fakeCurrent{id: gone, found: true},
			list: true, want: model.DefaultSessionRecord},
		{name: "a pointer to a session", current: fakeCurrent{id: exp.ID, found: true}, list: true, want: exp},
		{name: "a pointer to a deleted session is refused", current: fakeCurrent{id: gone, found: true},
			list: true, wantErr: model.ErrCurrentSessionNotFound},
		{name: "an unreadable pointer is refused", current: fakeCurrent{err: errPointer}, wantErr: errPointer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			sessions := &mockLister{}
			if tt.list {
				sessions.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord, exp}, nil).Once()
			}

			// Act
			got, err := Resolve(context.Background(), sessions, tt.current, tt.ref)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
			sessions.AssertExpectations(t)
		})
	}
}

func TestReadCurrent_UnreadableNamesTheWayOut(t *testing.T) {
	// Act
	_, _, err := ReadCurrent(context.Background(), fakeCurrent{err: errPointer})

	// Assert
	require.ErrorIs(t, err, errPointer)
	assert.Contains(t, err.Error(), "arcave session unset")
}

func mustSessionID(s string) model.SessionID {
	id, err := model.ParseSessionID(s)
	if err != nil {
		panic(err)
	}
	return id
}
