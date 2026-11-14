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

package sessionstore

import (
	"context"
	"strings"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/inmemory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/provider/compose/utils/composetest"
)

type mockGarbageFinder struct{ mock.Mock }

func (m *mockGarbageFinder) Find(
	ctx context.Context, store storages.Navigator, session model.SessionID, dataKinds []model.GarbageDataKind,
) []provider.Garbage {
	found, _ := m.Called(ctx, store, session, dataKinds).Get(0).([]provider.Garbage)
	return found
}

// newSessionStore is an arcave project store holding, in the default session, the compose projects
// names, each with one object.
func newSessionStore(t *testing.T, names ...string) storages.Storager {
	t.Helper()
	store := inmemory.New("")
	for _, name := range names {
		key := "sessions/" + model.DefaultSession.String() + "/compose/" + name + "/.keep"
		require.NoError(t, store.PutObject(t.Context(), key, strings.NewReader("")))
	}
	return store
}

// The compose projects of the session are its workloads, sorted, whatever they hold.
func TestSession_Workloads(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  []provider.WorkloadName
	}{
		{
			name:  "sorted",
			names: []string{"shop", "blog"},
			want:  []provider.WorkloadName{"blog", "shop"},
		},
		{
			name: "none",
			want: []provider.WorkloadName{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			session := New(Deps{
				Store:   newSessionStore(t, tt.names...),
				Session: model.DefaultSession,
			})

			// Act
			got, err := session.Workloads(context.Background())

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A workload's savepoints are the repository of its own compose project's store.
func TestSession_Savepoints(t *testing.T) {
	// Arrange
	savepoints := []model.Savepoint{{ID: "019e843e-1650-7000-8000-000000000001"}}
	records := &composetest.MockSavepointRepo{}
	records.On("ListSavepoints", mock.Anything).Return(savepoints, nil).Once()
	var openedAt string
	session := New(Deps{
		Store:   newSessionStore(t, "blog", "shop"),
		Session: model.DefaultSession,
		NewSavepointRecords: func(store storages.Storager) SavepointRecords {
			openedAt = store.Dirname()
			return records
		},
	})

	// Act
	got, err := session.Savepoints("shop")
	require.NoError(t, err)
	listed, listErr := got.List(context.Background())

	// Assert
	require.NoError(t, listErr)
	assert.Equal(t, "shop", openedAt)
	assert.Equal(t, savepoints, listed)
	records.AssertExpectations(t)
}

// A name the compose project gate refuses opens nothing.
func TestSession_Savepoints_RefusesABadName(t *testing.T) {
	// Arrange: no records are opened, so a call would panic the test.
	session := New(Deps{
		Store:   newSessionStore(t),
		Session: model.DefaultSession,
	})

	// Act
	got, err := session.Savepoints("../escape")

	// Assert
	require.Error(t, err)
	assert.Nil(t, got)
}

// The garbage is the finder's, among the data kinds asked; with no finder, none.
func TestSession_FindGarbage(t *testing.T) {
	tests := []struct {
		name      string
		hasFinder bool
	}{
		{name: "the finder's", hasFinder: true},
		{name: "no finder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			dataKinds := []model.GarbageDataKind{model.GarbageDataKindImageLayers}
			var (
				finder = &mockGarbageFinder{}
				want   []provider.Garbage
				store  = newSessionStore(t)
				deps   = Deps{Store: store, Session: model.DefaultSession}
			)
			if tt.hasFinder {
				want = []provider.Garbage{nil}
				finder.On("Find", mock.Anything, store, model.DefaultSession, dataKinds).Return(want).Once()
				deps.Garbage = finder
			}

			// Act
			got := New(deps).FindGarbage(context.Background(), dataKinds)

			// Assert
			assert.Equal(t, want, got)
			finder.AssertExpectations(t)
		})
	}
}

// A zero session is the default one.
func TestNew_ZeroSessionIsTheDefault(t *testing.T) {
	// Act
	got := New(Deps{}).ID()

	// Assert
	assert.Equal(t, model.DefaultSession, got)
}
