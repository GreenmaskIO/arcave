// Copyright 2025 Greenmask
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

package heartbeat

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// newStore is a real directory Storager on a temp dir. The reader and the writer
// are the two halves of one object protocol, so a fake would only re-implement
// the thing under test.
func newStore(t *testing.T) storages.Storager {
	t.Helper()

	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	return store
}

// putObject plants body at the heartbeat key, behind the writer's back — as a
// foreign writer or a torn write would have left it.
func putObject(t *testing.T, store storages.Storager, body string) {
	t.Helper()

	require.NoError(t, store.PutObject(context.Background(), FileName, strings.NewReader(body)))
}

// putHeartbeat plants a well-formed heartbeat carrying status and updatedAt.
func putHeartbeat(t *testing.T, store storages.Storager, status Status, updatedAt time.Time) {
	t.Helper()

	body, err := json.Marshal(Heartbeat{Status: status, UpdatedAt: updatedAt})
	require.NoError(t, err)
	putObject(t, store, string(body))
}

// readHeartbeat decodes whatever sits at the heartbeat key.
func readHeartbeat(t *testing.T, store storages.Storager) Heartbeat {
	t.Helper()

	obj, err := store.GetObject(context.Background(), FileName)
	require.NoError(t, err)
	defer func() { assert.NoError(t, obj.Close()) }()

	var got Heartbeat
	require.NoError(t, json.NewDecoder(obj).Decode(&got))
	return got
}

// exists reports whether the heartbeat object is there at all.
func exists(t *testing.T, store storages.Storager) bool {
	t.Helper()

	found, err := store.Exists(context.Background(), FileName)
	require.NoError(t, err)
	return found
}

// failingStore is a Storager whose object methods refuse with err. Every other
// method is the embedded nil interface, which these tests never reach.
type failingStore struct {
	storages.Storager
	err error
}

func (s failingStore) GetObject(context.Context, string) (io.ReadCloser, error) { return nil, s.err }

func (s failingStore) PutObject(context.Context, string, io.Reader) error { return s.err }

// mockHeartbeatWriter is the worker's writer seam.
type mockHeartbeatWriter struct {
	mock.Mock
}

func (m *mockHeartbeatWriter) Write(ctx context.Context, status Status) error {
	return m.Called(ctx, status).Error(0)
}
