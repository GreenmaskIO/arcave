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

package compose

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/volumebackend/cdc"
)

// newCDCOptions is the translation that keeps internal/config free of any package
// carrying a client: the config's own vocabulary in, the backend's own out.
func TestNewCDCOptions(t *testing.T) {
	tests := []struct {
		name string
		cfg  composeconfig.CDCBackend
		want cdc.Options
	}{
		{
			// A project that never names cdc carries no section, and still gets a
			// runnable image: an old cdc savepoint is rolled back through cdc
			// whatever snapshot.backend says now.
			name: "an absent section resolves to the defaults",
			cfg:  composeconfig.CDCWithDefaults(nil),
			want: cdc.Options{
				Image:       composeconfig.DefaultResticImage,
				Overwrite:   composeconfig.OverwriteAlways,
				Incremental: true,
			},
		},
		{
			name: "wipe is not incremental, whatever overwrite says",
			cfg: composeconfig.CDCBackend{
				RestoreMode: composeconfig.RestoreModeWipe,
				Overwrite:   composeconfig.OverwriteAlways,
				Image:       composeconfig.DefaultResticImage,
			},
			want: cdc.Options{
				Image:     composeconfig.DefaultResticImage,
				Overwrite: composeconfig.OverwriteAlways,
			},
		},
		{
			name: "incremental carries its overwrite policy through",
			cfg: composeconfig.CDCBackend{
				RestoreMode: composeconfig.RestoreModeIncremental,
				Overwrite:   composeconfig.OverwriteIfChanged,
				Image:       "restic/restic:0.18.0",
			},
			want: cdc.Options{
				Image:       "restic/restic:0.18.0",
				Incremental: true,
				Overwrite:   composeconfig.OverwriteIfChanged,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newCDCOptions(tt.cfg)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// newTestResticInspectorOpener is a tcp opener over a fresh directory store, its containers on
// the host network so that no daemon is asked.
func newTestResticInspectorOpener(t *testing.T) (resticInspectorOpener, storages.Storager) {
	t.Helper()
	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	cfg := composeconfig.CDCWithDefaults(&composeconfig.CDCBackend{
		Transport:   composeconfig.CDCTransportTCP,
		NetworkMode: composeconfig.NetworkModeHost,
	})
	return resticInspectorOpener{cfg: cfg}, store
}

// stdio serves the repositories over each container's exec stream and listens on nothing, so
// the tcp keys are not read; tcp binds the server and names it to the containers.
func TestOpenCDCTransport(t *testing.T) {
	tests := []struct {
		name    string
		cfg     composeconfig.CDCBackend
		verify  func(t *testing.T, opened openedCDCTransport)
		wantErr string
	}{
		{
			// auto would ask the nil engine and the address would not bind, so
			// either one read under stdio fails the open.
			name: "stdio is the default and reads no tcp key",
			cfg: composeconfig.CDCWithDefaults(&composeconfig.CDCBackend{
				NetworkMode:   composeconfig.NetworkModeAuto,
				ServerAddress: "192.0.2.1:1",
			}),
			verify: func(t *testing.T, opened openedCDCTransport) {
				transport, ok := opened.transport.(cdc.StdioTransport)
				require.True(t, ok, "transport = %T", opened.transport)
				assert.NotNil(t, transport.Server)
				assert.Nil(t, opened.server, "stdio listens on nothing")
				assert.NotPanics(t, func() { opened.Close(context.Background()) }, "Close has nothing to stop")
			},
		},
		{
			name: "tcp",
			cfg: composeconfig.CDCWithDefaults(&composeconfig.CDCBackend{
				Transport:   composeconfig.CDCTransportTCP,
				NetworkMode: composeconfig.NetworkModeHost,
			}),
			verify: func(t *testing.T, opened openedCDCTransport) {
				require.NotNil(t, opened.server)
				port := strconv.Itoa(opened.server.Listener.Addr().(*net.TCPAddr).Port)
				assert.Equal(t, cdc.TCPTransport{
					RepoURLBase: "rest:http://127.0.0.1:" + port,
					HostNetwork: true,
				}, opened.transport)
			},
		},
		{
			name:    "a transport arcave does not offer is refused",
			cfg:     composeconfig.CDCBackend{Transport: "ssh"},
			wantErr: `unsupported backends.cdc.transport "ssh"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := zerolog.Nop().WithContext(context.Background())
			store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
			require.NoError(t, err)

			// Act
			opened, err := openCDCTransport(ctx, tt.cfg, nil, store)

			// Assert
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { opened.Close(ctx) })
			tt.verify(t, opened)
		})
	}
}

// The opener serves the compose project's repositories while the inspector is open.
func TestResticInspectorOpener_Open(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	opener, store := newTestResticInspectorOpener(t)

	// Act
	inspector, err := opener.Open(ctx, store)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { inspector.Close(ctx) })
	conn, err := net.Dial("tcp", inspector.(resticInspector).server.Listener.Addr().String())
	require.NoError(t, err, "the server listens")
	assert.NoError(t, conn.Close())
}

// An address the server cannot bind fails the open.
func TestResticInspectorOpener_Open_AddressTaken(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	opener, store := newTestResticInspectorOpener(t)
	opener.cfg.ServerAddress = listener.Addr().String()

	// Act
	_, err = opener.Open(ctx, store)

	// Assert
	assert.ErrorContains(t, err, "start the restic server")
}

// Close stops the server the open started.
func TestResticInspector_Close(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	opener, store := newTestResticInspectorOpener(t)
	inspector, err := opener.Open(ctx, store)
	require.NoError(t, err)
	listener := inspector.(resticInspector).server.Listener

	// Act
	inspector.Close(ctx)

	// Assert: a dial could reach whatever took the freed port since.
	_, err = listener.Accept()
	assert.ErrorIs(t, err, net.ErrClosed, "nothing listens once closed")
}

// The pruner opener serves the compose project's repositories while the pruner is open.
func TestResticPrunerOpener_Open(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	inspectorOpener, store := newTestResticInspectorOpener(t)
	opener := resticPrunerOpener{cfg: inspectorOpener.cfg}

	// Act
	pruner, err := opener.Open(ctx, store)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { pruner.Close(ctx) })
	conn, err := net.Dial("tcp", pruner.(resticPruner).server.Listener.Addr().String())
	require.NoError(t, err, "the server listens")
	assert.NoError(t, conn.Close())
}

// Close stops the server the open started.
func TestResticPruner_Close(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	inspectorOpener, store := newTestResticInspectorOpener(t)
	pruner, err := resticPrunerOpener{cfg: inspectorOpener.cfg}.Open(ctx, store)
	require.NoError(t, err)
	listener := pruner.(resticPruner).server.Listener

	// Act
	pruner.Close(ctx)

	// Assert: a dial could reach whatever took the freed port since.
	_, err = listener.Accept()
	assert.ErrorIs(t, err, net.ErrClosed, "nothing listens once closed")
}

// The served repositories are the compose project store's restic/ namespace, as objects too: a
// pruner clears the locks it finds there.
func TestOpenResticRepositories(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	opener, store := newTestResticInspectorOpener(t)
	require.NoError(t, store.PutObject(ctx, "restic/volume-named-db/locks/a1", strings.NewReader("orphan")))

	// Act
	served, err := openResticRepositories(ctx, opener.cfg, nil, store)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { served.Close(ctx) })
	objects, err := served.root.List(ctx, "")
	require.NoError(t, err)
	require.Len(t, objects, 1)
	assert.Equal(t, "volume-named-db/locks/a1", objects[0].Name)
	transport, ok := served.transport.(cdc.TCPTransport)
	require.True(t, ok, "transport = %T", served.transport)
	assert.Contains(t, transport.RepoURLBase, strconv.Itoa(served.server.Listener.Addr().(*net.TCPAddr).Port))
}
