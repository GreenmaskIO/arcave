package corev2

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

type mockInspector struct{ mock.Mock }

func (m *mockInspector) Introspect(ctx context.Context, files ...string) (model.IntrospectionResult, error) {
	args := m.Called(ctx, files)
	return args.Get(0).(model.IntrospectionResult), args.Error(1)
}

type snapshotCall struct {
	kind        model.SnapshotBackend
	objectID    string
	savepointID string
}

// snapshotRecorder collects the specs the factory was handed and the calls the
// backends took; Snapshot runs concurrently, so both are guarded.
type snapshotRecorder struct {
	mu        sync.Mutex
	mintSpecs []BackendSpec
	calls     []snapshotCall
}

func (r *snapshotRecorder) mint(spec BackendSpec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mintSpecs = append(r.mintSpecs, spec)
}

func (r *snapshotRecorder) specs() []BackendSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]BackendSpec(nil), r.mintSpecs...)
}

func (r *snapshotRecorder) record(call snapshotCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

type recordingFactory struct {
	kind     model.SnapshotBackend
	recorder *snapshotRecorder
}

func (f recordingFactory) Kind() model.SnapshotBackend { return f.kind }

func (f recordingFactory) New(spec BackendSpec) (VolumeSnapshotBackend, error) {
	f.recorder.mint(spec)
	return recordingBackend{kind: f.kind, spec: spec, recorder: f.recorder}, nil
}

type recordingBackend struct {
	kind     model.SnapshotBackend
	spec     BackendSpec
	recorder *snapshotRecorder
}

func (b recordingBackend) Snapshot(context.Context) (model.SnapshotStat, error) {
	b.recorder.record(snapshotCall{
		kind:        b.kind,
		objectID:    b.spec.Volume.ObjectID,
		savepointID: b.spec.SavepointID,
	})
	return model.SnapshotStat{Volume: b.spec.Volume, Type: b.kind}, nil
}

func TestService_CreateSavepoint(t *testing.T) {
	// One named volume and one bind mount are units; the named volume's
	// service-level entry is only a reference, and the tmpfs is a skip.
	project := model.Project{
		Name: "demo",
		Services: []model.Service{
			{
				Name: "app",
				Volumes: []model.ServiceVolume{
					{
						Type:    model.VolumeTypeBind,
						RawType: "bind",
						Source:  "/host/cfg",
						Target:  "/etc/cfg",
					},
					{
						Type:    model.VolumeTypeTmpfs,
						RawType: "tmpfs",
						Target:  "/scratch",
					},
				},
			},
			{
				Name: "db",
				Volumes: []model.ServiceVolume{{
					Type:       model.VolumeTypeNamed,
					RawType:    "volume",
					Source:     "pgdata",
					Target:     "/var/lib/postgresql/data",
					VolumeName: "demo_pgdata",
				}},
			},
		},
		Volumes: []model.NamedVolume{{
			Key:    "pgdata",
			Name:   "demo_pgdata",
			UsedBy: []model.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}},
		}},
	}

	store := sessionStore(t)

	tests := []struct {
		name      string
		factories []model.SnapshotBackend
		wantErr   error
		verify    func(t *testing.T, savepoint model.Savepoint, recorder *snapshotRecorder, logs string)
	}{
		{
			name:      "every volume snapshots through its backend under one savepoint id, and the skipped mount is warned about",
			factories: []model.SnapshotBackend{model.SnapshotBackendFullCopy},
			verify: func(t *testing.T, savepoint model.Savepoint, recorder *snapshotRecorder, logs string) {
				assert.Contains(t, logs, "mount will not be backed up")
				assert.Contains(t, logs, "/scratch")
				assert.Contains(t, logs, "tmpfs")

				require.Len(t, recorder.calls, 2)
				byObjectID := map[string]snapshotCall{}
				for _, call := range recorder.calls {
					byObjectID[call.objectID] = call
				}
				require.Contains(t, byObjectID, "volume/demo_pgdata")
				require.Contains(t, byObjectID, "service/app/etc/cfg")
				assert.Equal(t, model.SnapshotBackendFullCopy, byObjectID["volume/demo_pgdata"].kind)
				assert.Equal(t, model.SnapshotBackendFullCopy, byObjectID["service/app/etc/cfg"].kind)

				require.NoError(t, model.ValidSavepointID(savepoint.ID))
				assert.Equal(t, savepoint.ID, byObjectID["volume/demo_pgdata"].savepointID)
				assert.Equal(t, savepoint.ID, byObjectID["service/app/etc/cfg"].savepointID)

				// Every backend was minted against the savepoint store, rooted where
				// the factory left it: no layer above a backend narrows a store.
				specs := recorder.specs()
				require.Len(t, specs, 2)
				for _, spec := range specs {
					assert.Same(t, store, spec.SessionStore)
					assert.Equal(t, savepoint.ID, spec.SavepointID, "the id is minted before the backends are")
				}

				assert.Equal(t, "before-migration", savepoint.Name)
				assert.Equal(t, "trying a schema change", savepoint.Description)
				assert.False(t, savepoint.CreatedAt.IsZero())

				require.Len(t, savepoint.Stats, 2)
				statObjectIDs := []string{savepoint.Stats[0].Volume.ObjectID, savepoint.Stats[1].Volume.ObjectID}
				assert.ElementsMatch(t, []string{"volume/demo_pgdata", "service/app/etc/cfg"}, statObjectIDs)
			},
		},
		{
			name:      "a backend without a factory fails before any snapshot",
			factories: nil,
			wantErr:   ErrUnknownBackend,
			verify: func(t *testing.T, _ model.Savepoint, recorder *snapshotRecorder, _ string) {
				assert.Empty(t, recorder.calls)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			inspector := &mockInspector{}
			inspector.On("Introspect", mock.Anything, []string{"docker-compose.yml"}).
				Return(model.IntrospectionResult{Project: project}, nil)

			recorder := &snapshotRecorder{}
			factories := make([]SnapshotBackendFactory, 0, len(tt.factories))
			for _, kind := range tt.factories {
				factories = append(factories, recordingFactory{kind: kind, recorder: recorder})
			}
			registry, err := NewSnapshotBackendRegistry(factories...)
			require.NoError(t, err)

			// The safety-backup store is nil: CreateSavepoint must never reach for
			// it, and a nil store would be a nil deref if it did.
			svc := New(nil, nil, inspector, nil, registry, store, nil)

			logs := &bytes.Buffer{}
			ctx := zerolog.New(logs).WithContext(context.Background())

			// Act
			savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{
				ComposeFiles: []string{"docker-compose.yml"},
				Name:         "before-migration",
				Description:  "trying a schema change",
			})

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			tt.verify(t, savepoint, recorder, logs.String())
			inspector.AssertExpectations(t)
		})
	}
}
