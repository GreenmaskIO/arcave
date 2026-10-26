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
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
)

func TestMCPHandler_createSavepointMCP(t *testing.T) {
	// Arrange
	savepoint := model.Savepoint{ID: "019e843e-1650-7000-8000-000000000000", Name: "before-migration", NotCovered: &model.NotCovered{
		Mounts: []model.SkippedVolume{{Service: "app", Target: "/scratch", RawType: "tmpfs",
			Reason: model.SkipReasonTmpfs, Detail: "non-persistent (tmpfs lives in RAM)"}},
		ServicesWithoutVolumes: []string{"web"},
		KilledContainers:       []model.KilledContainer{{Service: "db", Replica: 1, Container: "demo-db-1"}},
	}}
	svc := &mockService{}
	svc.On("CreateSavepoint", mock.Anything, model.CreateSavepointRequest{Name: "before-migration"}).
		Return(savepoint, nil).Once()
	released := false
	h := NewMCPHandler(func(context.Context, string, string) (MCPService, func(), error) {
		return svc, func() { released = true }, nil
	}, nil, nil, nil, model.SessionID{}, model.LeftoverVolumesOverwrite)

	// Act
	res, out, err := h.createSavepointMCP(context.Background(), nil, createInput{Project: "demo", Name: "before-migration"})

	// Assert
	require.NoError(t, err)
	assert.Nil(t, res)
	assert.Equal(t, savepoint, out)
	assert.True(t, released, "what the call opened must not outlive it")
	svc.AssertExpectations(t)
}

func TestMCPHandler_listSavepointsMCP(t *testing.T) {
	savepoints := []model.Savepoint{{ID: "019e843e-1650-7000-8000-000000000000", Name: "before-migration"}}

	tests := []struct {
		name string
		in   listSavepointInput
		sort model.SortOrder
		svc  []model.Savepoint
		want listOutput
	}{
		{name: "hidden by default", in: listSavepointInput{Project: "demo"}, sort: model.SortDesc, svc: savepoints,
			want: listOutput{Savepoints: savepoints}},
		{
			name: "include_safety_snapshots reaches the service",
			in:   listSavepointInput{Project: "demo", IncludeSafetySnapshots: true},
			sort: model.SortDesc,
			svc:  savepoints,
			want: listOutput{Savepoints: savepoints},
		},
		{
			name: "sort reaches the service",
			in:   listSavepointInput{Project: "demo", Sort: "asc"},
			sort: model.SortAsc,
			svc:  savepoints,
			want: listOutput{Savepoints: savepoints},
		},
		{
			name: "no savepoints answers an empty array",
			in:   listSavepointInput{Project: "demo"},
			sort: model.SortDesc,
			want: listOutput{Savepoints: []model.Savepoint{}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc := &mockService{}
			svc.On("ListSavepoints", mock.Anything,
				model.ListRequest{IncludeSafetySnapshots: tt.in.IncludeSafetySnapshots, Sort: tt.sort}).
				Return(tt.svc, nil).Once()
			var gotProject string
			released := false
			factory := func(_ context.Context, project, _ string) (MCPService, func(), error) {
				gotProject = project
				return svc, func() { released = true }, nil
			}
			h := NewMCPHandler(factory, nil, nil, nil, model.SessionID{}, model.LeftoverVolumesOverwrite)

			// Act
			res, out, err := h.listSavepointsMCP(context.Background(), nil, tt.in)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, out)
			assert.Nil(t, res)
			assert.Equal(t, tt.in.Project, gotProject)
			assert.True(t, released, "what the call opened must not outlive it")
			svc.AssertExpectations(t)
		})
	}
}

func TestMCPHandler_listSavepointsMCP_ABadSortIsRefused(t *testing.T) {
	// Arrange: a factory that fails the test, so a refusal must come before anything is opened.
	h := NewMCPHandler(func(context.Context, string, string) (MCPService, func(), error) {
		t.Fatal("a bad sort must not open the store")
		return nil, nil, nil
	}, nil, nil, nil, model.SessionID{}, model.LeftoverVolumesOverwrite)

	// Act
	_, _, err := h.listSavepointsMCP(context.Background(), nil, listSavepointInput{Project: "demo", Sort: "newest"})

	// Assert
	require.ErrorIs(t, err, model.ErrInvalidSortOrder)
}

func TestMCPHandler_listSavepointsMCP_AFactoryFailureSurfaces(t *testing.T) {
	// Arrange
	wireErr := errors.New("no such project")
	svc := &mockService{}
	h := NewMCPHandler(func(context.Context, string, string) (MCPService, func(), error) {
		return nil, nil, wireErr
	}, nil, nil, nil, model.SessionID{}, model.LeftoverVolumesOverwrite)

	// Act
	_, out, err := h.listSavepointsMCP(context.Background(), nil, listSavepointInput{Project: "nope"})

	// Assert
	require.ErrorIs(t, err, wireErr)
	assert.Equal(t, listOutput{}, out)
	svc.AssertExpectations(t)
}

func TestMCPHandler_openService_SessionPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		pin     model.SessionID
		session string
		want    string
	}{
		{name: "neither follows the current session", want: ""},
		{name: "the pin, by id", pin: handlerSession, want: handlerSession.String()},
		{name: "the call outranks the pin", pin: handlerSession, session: "baseline", want: "baseline"},
		{name: "the call alone", session: "exp", want: "exp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc := &mockService{}
			svc.On("ListSavepoints", mock.Anything, model.ListRequest{Sort: model.SortDesc}).Return([]model.Savepoint(nil), nil).Once()
			var gotSession string
			factory := func(_ context.Context, _, session string) (MCPService, func(), error) {
				gotSession = session
				return svc, func() {}, nil
			}
			h := NewMCPHandler(factory, nil, nil, nil, tt.pin, model.LeftoverVolumesOverwrite)

			// Act
			_, _, err := h.listSavepointsMCP(context.Background(), nil, listSavepointInput{Project: "demo", Session: tt.session})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, gotSession)
			svc.AssertExpectations(t)
		})
	}
}

func TestMCPHandler_ALockedProjectIsNamed(t *testing.T) {
	// Arrange
	svc := &mockService{}
	svc.On("Rollback", mock.Anything, mock.Anything).Return(model.RollbackResult{}, model.ErrProjectLocked).Once()
	h := NewMCPHandler(func(context.Context, string, string) (MCPService, func(), error) {
		return svc, func() {}, nil
	}, nil, nil, nil, model.SessionID{}, model.LeftoverVolumesOverwrite)

	// Act
	_, _, err := h.rollbackMCP(context.Background(), nil, rollbackInput{Project: "demo", Ref: "latest"})

	// Assert
	require.ErrorIs(t, err, model.ErrProjectLocked)
	assert.Contains(t, err.Error(), "session fork")
	svc.AssertExpectations(t)
}

// sessionFactoryOf hands svc to every call and records whether the call released it.
func sessionFactoryOf(svc MCPSessionService, released *bool) MCPSessionServiceFactory {
	return func(context.Context) (MCPSessionService, func(), error) {
		return svc, func() { *released = true }, nil
	}
}

func TestMCPHandler_listSessionsMCP(t *testing.T) {
	sessions := []model.SessionSummary{{Session: model.DefaultSessionRecord, Current: true}}
	tests := []struct {
		name string
		svc  []model.SessionSummary
		want sessionListOutput
	}{
		{name: "every session", svc: sessions, want: sessionListOutput{Sessions: sessions}},
		{name: "none answers an empty array", want: sessionListOutput{Sessions: []model.SessionSummary{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc := &mockService{}
			svc.On("ListSessions", mock.Anything, model.ListSessionsRequest{All: true, Sort: model.SortDesc}).Return(tt.svc, nil).Once()
			released := false
			h := NewMCPHandler(nil, sessionFactoryOf(svc, &released), nil, nil, model.SessionID{}, model.LeftoverVolumesOverwrite)

			// Act
			_, out, err := h.listSessionsMCP(context.Background(), nil, sessionListInput{All: true})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, out)
			assert.True(t, released, "what the call opened must not outlive it")
			svc.AssertExpectations(t)
		})
	}
}

func TestMCPHandler_createSessionMCP(t *testing.T) {
	// Arrange
	record := model.Session{ID: handlerSession, Name: "exp", From: &model.SessionOrigin{Session: model.DefaultSession}}
	svc := &mockService{}
	svc.On("CreateSession", mock.Anything, model.CreateSessionRequest{Name: "exp", From: "baseline"}).Return(record, nil).Once()
	released := false
	h := NewMCPHandler(nil, sessionFactoryOf(svc, &released), nil, nil, model.SessionID{}, model.LeftoverVolumesOverwrite)

	// Act
	_, out, err := h.createSessionMCP(context.Background(), nil, sessionCreateInput{Name: "exp", From: "baseline"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, record, out)
	assert.True(t, released)
	svc.AssertExpectations(t)
}

func TestMCPHandler_removeSessionMCP(t *testing.T) {
	// Arrange
	res := model.RemoveSessionResponse{Session: handlerSession, Removed: true, FreedBytes: 42}
	svc := &mockService{}
	svc.On("RemoveSession", mock.Anything, model.RemoveSessionRequest{Session: "exp", Force: true}).Return(res, nil).Once()
	released := false
	h := NewMCPHandler(nil, sessionFactoryOf(svc, &released), nil, nil, model.SessionID{}, model.LeftoverVolumesOverwrite)

	// Act
	_, out, err := h.removeSessionMCP(context.Background(), nil, sessionRemoveInput{Session: "exp", Force: true})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, res, out)
	assert.True(t, released)
	svc.AssertExpectations(t)
}

func TestMCPHandler_setSessionMCP(t *testing.T) {
	// Arrange
	record := model.Session{ID: handlerSession, Name: "exp"}
	svc := &mockService{}
	svc.On("SetSession", mock.Anything, "exp").Return(record, nil).Once()
	released := false
	h := NewMCPHandler(nil, sessionFactoryOf(svc, &released), nil, nil, model.SessionID{}, model.LeftoverVolumesOverwrite)

	// Act
	_, out, err := h.setSessionMCP(context.Background(), nil, sessionSetInput{Session: "exp"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, record, out)
	assert.True(t, released)
	svc.AssertExpectations(t)
}

func TestMCPHandler_setSessionMCP_RefusedWhenPinned(t *testing.T) {
	// Arrange: strict mock, so a pinned server that writes the pointer fails the test.
	svc := &mockService{}
	released := false
	h := NewMCPHandler(nil, sessionFactoryOf(svc, &released), nil, nil, handlerSession, model.LeftoverVolumesOverwrite)

	// Act
	_, _, err := h.setSessionMCP(context.Background(), nil, sessionSetInput{Session: "exp"})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), `pinned to session "`+handlerSession.String()+`"`)
	assert.False(t, released, "nothing is opened")
	svc.AssertExpectations(t)
}

func TestNewOutputSchema_ValidatesWhatASessionToolReturns(t *testing.T) {
	// Arrange: every SessionID position — a record's id, its origin, a listing row, a delete response.
	record := model.Session{ID: handlerSession, Name: "exp", From: &model.SessionOrigin{Session: model.DefaultSession}}
	tests := []struct {
		name   string
		schema *jsonschema.Schema
		value  any
	}{
		{name: "a record", schema: newOutputSchema[model.Session](), value: record},
		{name: "a listing", schema: newOutputSchema[sessionListOutput](),
			value: sessionListOutput{Sessions: []model.SessionSummary{{Session: record, Current: true}}}},
		{name: "a delete", schema: newOutputSchema[model.RemoveSessionResponse](),
			value: model.RemoveSessionResponse{Session: handlerSession, Removed: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := tt.schema.Resolve(nil)
			require.NoError(t, err)
			data, err := json.Marshal(tt.value)
			require.NoError(t, err)
			var instance any
			require.NoError(t, json.Unmarshal(data, &instance))

			// Act
			err = resolved.Validate(instance)

			// Assert
			require.NoError(t, err)
		})
	}
}

func TestMCPHandler_unsetSessionMCP(t *testing.T) {
	// Arrange
	svc := &mockService{}
	svc.On("UnsetSession", mock.Anything).Return(model.DefaultSessionRecord, nil).Once()
	released := false
	h := NewMCPHandler(nil, sessionFactoryOf(svc, &released), nil, nil, model.SessionID{}, model.LeftoverVolumesOverwrite)

	// Act
	_, out, err := h.unsetSessionMCP(context.Background(), nil, struct{}{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.DefaultSessionRecord, out)
	assert.True(t, released)
	svc.AssertExpectations(t)
}

func TestMCPHandler_unsetSessionMCP_RefusedWhenPinned(t *testing.T) {
	// Arrange: strict mock, so a pinned server that clears the pointer fails the test.
	svc := &mockService{}
	released := false
	h := NewMCPHandler(nil, sessionFactoryOf(svc, &released), nil, nil, handlerSession, model.LeftoverVolumesOverwrite)

	// Act
	_, _, err := h.unsetSessionMCP(context.Background(), nil, struct{}{})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), `pinned to session "`+handlerSession.String()+`"`)
	assert.False(t, released, "nothing is opened")
	svc.AssertExpectations(t)
}

func TestMCPHandler_currentSessionMCP(t *testing.T) {
	tests := []struct {
		name string
		pin  model.SessionID
		ref  string
	}{
		{name: "unpinned follows the pointer"},
		{name: "pinned answers the pin, by id", pin: handlerSession, ref: handlerSession.String()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := model.Session{ID: handlerSession, Name: "feat"}
			svc := &mockService{}
			svc.On("ResolveSession", mock.Anything, tt.ref).Return(record, nil).Once()
			released := false
			h := NewMCPHandler(nil, sessionFactoryOf(svc, &released), nil, nil, tt.pin, model.LeftoverVolumesOverwrite)

			// Act
			_, out, err := h.currentSessionMCP(context.Background(), nil, struct{}{})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, record, out)
			assert.True(t, released)
			svc.AssertExpectations(t)
		})
	}
}

func TestMCPHandler_PinnedSessionTools(t *testing.T) {
	// Arrange
	svc := &mockService{}
	svc.On("ListSessions", mock.Anything, model.ListSessionsRequest{Ref: handlerSession.String(), Sort: model.SortDesc}).
		Return([]model.SessionSummary(nil), nil).Once()
	svc.On("RemoveSession", mock.Anything, model.RemoveSessionRequest{Session: "feat", Pinned: handlerSession}).
		Return(model.RemoveSessionResponse{}, model.ErrCurrentSessionDelete).Once()
	released := false
	h := NewMCPHandler(nil, sessionFactoryOf(svc, &released), nil, nil, handlerSession, model.LeftoverVolumesOverwrite)

	// Act
	_, _, listErr := h.listSessionsMCP(context.Background(), nil, sessionListInput{})
	_, _, deleteErr := h.removeSessionMCP(context.Background(), nil, sessionRemoveInput{Session: "feat"})

	// Assert: the listing marks the pin, and the pin is protected like the current session.
	require.NoError(t, listErr)
	require.ErrorIs(t, deleteErr, model.ErrCurrentSessionDelete)
	svc.AssertExpectations(t)
}

func TestMCPHandler_Server_EveryTool(t *testing.T) {
	savepoint := model.Savepoint{ID: "019e843e-1650-7000-8000-000000000000", Name: "v1"}
	record := model.Session{ID: handlerSession, Name: "feat", From: &model.SessionOrigin{Session: model.DefaultSession}}
	// A partial gc is still a report; its session marshals as the string the schema says.
	gcResult := model.GarbageCollectResult{DryRun: true, Session: handlerSession, Reports: []model.GarbageReport{{
		DataKind: model.GarbageDataKindImageLayers, ComposeProject: "demo",
		Summary: model.GarbageSummary{Aborted: "1 manifests of committed savepoints cannot be read"},
	}}, Total: model.GarbageTotal{Aborted: 1}}
	layers := []model.WritableLayerRestoreResult{{Service: "db", Replica: 1, ContainerName: "demo-db-1",
		ImageID: model.ImageDigestID("sha256:" + strings.Repeat("a", 64)), Tag: "arcave/demo-db-1-postgres-16:sp-v1"}}
	tests := []struct {
		tool  string
		args  map[string]any
		setup func(svc *mockService)
		want  any // the structured content, decoded into its type
	}{
		{tool: "savepoint_create", args: map[string]any{"project": "demo", "name": "v1"},
			setup: func(svc *mockService) {
				svc.On("CreateSavepoint", mock.Anything, model.CreateSavepointRequest{Name: "v1"}).Return(savepoint, nil).Once()
			}, want: savepoint},
		{tool: "savepoint_list", args: map[string]any{"project": "demo", "session": "feat"},
			setup: func(svc *mockService) {
				svc.On("ListSavepoints", mock.Anything, model.ListRequest{Sort: model.SortDesc}).Return([]model.Savepoint{savepoint}, nil).Once()
			}, want: listOutput{Savepoints: []model.Savepoint{savepoint}}},
		{tool: "savepoint_remove", args: map[string]any{"project": "demo", "ids": []string{savepoint.ID.String()}},
			setup: func(svc *mockService) {
				svc.On("RemoveSavepoints", mock.Anything, model.RemoveRequest{IDs: []model.SavepointID{savepoint.ID}}).
					Return(model.RemoveResult{}, nil).Once()
			}, want: model.RemoveResponse{}},
		{tool: "rollback", args: map[string]any{"project": "demo", "ref": "latest"},
			setup: func(svc *mockService) {
				svc.On("Rollback", mock.Anything, mock.Anything).
					Return(model.RollbackResult{Savepoint: savepoint, WritableLayers: layers}, nil).Once()
			}, want: model.RollbackResponse{RolledBackTo: savepoint, WritableLayers: layers}},
		{tool: "session_list", args: map[string]any{"all": true},
			setup: func(svc *mockService) {
				svc.On("ListSessions", mock.Anything, model.ListSessionsRequest{All: true, Sort: model.SortDesc}).
					Return([]model.SessionSummary{{Session: record, Status: model.SessionStatusReady, Current: true}}, nil).Once()
			}, want: sessionListOutput{Sessions: []model.SessionSummary{{Session: record, Status: model.SessionStatusReady, Current: true}}}},
		{tool: "session_create", args: map[string]any{"name": "feat", "from": "baseline"},
			setup: func(svc *mockService) {
				svc.On("CreateSession", mock.Anything, model.CreateSessionRequest{Name: "feat", From: "baseline"}).Return(record, nil).Once()
			}, want: record},
		{tool: "session_remove", args: map[string]any{"session": "feat", "force": true},
			setup: func(svc *mockService) {
				svc.On("RemoveSession", mock.Anything, model.RemoveSessionRequest{Session: "feat", Force: true}).
					Return(model.RemoveSessionResponse{Session: handlerSession, Removed: true, FreedBytes: 42}, nil).Once()
			}, want: model.RemoveSessionResponse{Session: handlerSession, Removed: true, FreedBytes: 42}},
		{tool: "session_current", args: map[string]any{},
			setup: func(svc *mockService) {
				svc.On("ResolveSession", mock.Anything, "").Return(record, nil).Once()
			}, want: record},
		{tool: "session_set", args: map[string]any{"session": "feat"},
			setup: func(svc *mockService) {
				svc.On("SetSession", mock.Anything, "feat").Return(record, nil).Once()
			}, want: record},
		{tool: "session_unset", args: map[string]any{},
			setup: func(svc *mockService) {
				svc.On("UnsetSession", mock.Anything).Return(model.DefaultSessionRecord, nil).Once()
			}, want: model.DefaultSessionRecord},
		{tool: "compose_generate", args: map[string]any{"project": "demo", "absolute_paths": true},
			setup: func(svc *mockService) {
				svc.On("GenerateCompose", mock.Anything, model.GenerateComposeRequest{AbsolutePaths: true}).
					Return(model.GenerateComposeResponse{ComposeProject: "demo", Services: []string{"db"}, Spec: []byte("name: demo\n")}, nil).Once()
			}, want: composeGenerateOutput{ComposeProject: "demo", Services: []string{"db"}, Spec: "name: demo\n"}},
		{tool: "gc", args: map[string]any{"restored_layer_grace": "1h", "kinds": []string{"upstream-images"}},
			setup: func(svc *mockService) {
				svc.On("GarbageCollect", mock.Anything, model.GarbageCollectRequest{
					DryRun: true, RestoredLayerGrace: utils.New(time.Hour), DataKinds: []model.GarbageDataKind{model.GarbageDataKindUpstreamImages},
				}).
					Return(gcResult, model.ErrGarbageCollectPartial).Once()
			}, want: gcResult},
	}

	// Arrange: one server for every case, over the SDK's own transport, so its schema checks run.
	svc := &mockService{}
	for _, tt := range tests {
		tt.setup(svc)
	}
	h := NewMCPHandler(func(context.Context, string, string) (MCPService, func(), error) {
		return svc, func() {}, nil
	}, func(context.Context) (MCPSessionService, func(), error) {
		return svc, func() {}, nil
	}, func(context.Context, string) (MCPToolsService, func(), error) {
		return svc, func() {}, nil
	}, func(context.Context, string) (MCPGCService, func(), error) {
		return svc, func() {}, nil
	}, model.SessionID{}, model.LeftoverVolumesOverwrite)
	ctx := context.Background()
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := h.Server().Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	listed, err := cs.ListTools(ctx, nil)
	require.NoError(t, err)
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	var want []string
	for _, tt := range tests {
		want = append(want, tt.tool)
	}
	assert.ElementsMatch(t, want, names, "every tool is registered and every registered tool is covered")

	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			// Act
			res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: tt.tool, Arguments: tt.args})

			// Assert
			require.NoError(t, err)
			require.False(t, res.IsError, "tool error: %v", res.Content)
			data, err := json.Marshal(res.StructuredContent)
			require.NoError(t, err)
			got := reflect.New(reflect.TypeOf(tt.want))
			require.NoError(t, json.Unmarshal(data, got.Interface()))
			assert.Equal(t, tt.want, got.Elem().Interface())
		})
	}
	svc.AssertExpectations(t)
}

// dry_run left out reports only; false reaches the request and deletes. A gc that fails past a
// deletion still hands back what it deleted, as a tool error.
func TestMCPHandler_garbageCollectMCP(t *testing.T) {
	deleted := model.GarbageCollectResult{Session: handlerSession, Reports: []model.GarbageReport{{
		DataKind: model.GarbageDataKindSavepointData,
		Deletion: &model.GarbageDeletion{NotRun: 1},
	}}}

	tests := []struct {
		name        string
		in          gcInput
		res         model.GarbageCollectResult
		svcErr      error
		wantDryRun  bool
		wantErr     error
		wantIsError bool
		want        model.GarbageCollectResult
	}{
		{name: "dry_run left out is a dry run", wantDryRun: true},
		{name: "dry_run true is a dry run", in: gcInput{DryRun: utils.New(true)}, wantDryRun: true},
		{name: "dry_run false deletes", in: gcInput{DryRun: utils.New(false)}, res: deleted, want: deleted},
		{
			name: "a lock lost midway through deleting is a tool error carrying the result",
			in:   gcInput{DryRun: utils.New(false)}, res: deleted, svcErr: &model.GarbageCollectInterruptedError{Cause: model.ErrSessionLockLost},
			wantIsError: true, want: deleted,
		},
		{
			name: "a failure before any report is the call's error",
			in:   gcInput{DryRun: utils.New(false)}, svcErr: model.ErrSessionLocked, wantErr: model.ErrSessionLocked,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc := &mockService{}
			svc.On("GarbageCollect", mock.Anything, model.GarbageCollectRequest{DryRun: tt.wantDryRun}).
				Return(tt.res, tt.svcErr).Once()
			h := NewMCPHandler(nil, nil, nil, func(context.Context, string) (MCPGCService, func(), error) {
				return svc, func() {}, nil
			}, model.SessionID{}, model.LeftoverVolumesOverwrite)

			// Act
			res, got, err := h.garbageCollectMCP(context.Background(), nil, tt.in)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantIsError, res != nil && res.IsError)
			assert.Equal(t, tt.want, got)
			svc.AssertExpectations(t)
		})
	}
}

// How a rollback tool call ends decides whether its context is cancelled — and so whether
// core reverts — but never lets the server exit while the call is still unwinding.
func TestMCPHandler_Server_RollbackInterrupted(t *testing.T) {
	tests := []struct {
		name string
		// end ends the call, the server's stdin or the server while the rollback runs.
		end           func(cancelCall, closeStdin, stopServer func())
		wantCancelled bool
		wantServing   bool
	}{
		{
			name:          "the client cancels the call",
			end:           func(cancelCall, _, _ func()) { cancelCall() },
			wantCancelled: true,
			wantServing:   true,
		},
		{
			name:          "the client closes stdin",
			end:           func(_, closeStdin, _ func()) { closeStdin() },
			wantCancelled: true,
		},
		{
			name: "the server is interrupted",
			end:  func(_, _, stopServer func()) { stopServer() },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			started, cancelled, unwound := make(chan struct{}), make(chan bool, 1), make(chan struct{})
			svc := &mockService{}
			svc.On("Rollback", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				ctx := args.Get(0).(context.Context)
				close(started)
				select {
				case <-ctx.Done():
					cancelled <- true
				case <-unwound:
					cancelled <- false
				}
				<-unwound // the revert core runs on its own context
			}).Return(model.RollbackResult{}, context.Canceled).Once()
			if tt.wantServing {
				svc.On("ListSavepoints", mock.Anything, model.ListRequest{Sort: model.SortDesc}).Return([]model.Savepoint{}, nil).Once()
			}
			h := NewMCPHandler(func(context.Context, string, string) (MCPService, func(), error) {
				return svc, func() {}, nil
			}, nil, nil, nil, model.SessionID{}, model.LeftoverVolumesOverwrite)

			// Pipes rather than the in-memory pair: an agent host that dies closes the server's
			// stdin under a call in flight, which the SDK client's own Close never does.
			stdinR, stdinW := io.Pipe()
			stdoutR, stdoutW := io.Pipe()
			serverTransport := &mcpsdk.IOTransport{Reader: stdinR, Writer: stdoutW}
			clientTransport := &mcpsdk.IOTransport{Reader: stdoutR, Writer: stdinW}
			closeStdin := func() { _ = stdinW.Close() }
			serverCtx, stopServer := context.WithCancel(context.Background())
			defer stopServer()
			served := make(chan error, 1)
			go func() { served <- h.Server().Run(serverCtx, serverTransport) }()

			client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
			cs, err := client.Connect(context.Background(), clientTransport, nil)
			require.NoError(t, err)
			defer func() { _ = cs.Close() }()
			callCtx, cancelCall := context.WithCancel(context.Background())
			defer cancelCall()
			go func() {
				_, _ = cs.CallTool(callCtx, &mcpsdk.CallToolParams{
					Name: "rollback", Arguments: map[string]any{"project": "demo", "ref": "latest"},
				})
			}()
			<-started

			// Act
			tt.end(cancelCall, closeStdin, stopServer)

			// Assert
			if tt.wantCancelled {
				assert.True(t, <-cancelled, "the call's context must be cancelled, so core reverts")
			}
			select {
			case err := <-served:
				t.Fatalf("the server exited while the call was unwinding: %v", err)
			default:
			}
			close(unwound)
			if !tt.wantCancelled {
				assert.False(t, <-cancelled, "an interrupted server lets the call in flight finish")
			}

			if tt.wantServing {
				_, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
					Name: "savepoint_list", Arguments: map[string]any{"project": "demo"},
				})
				require.NoError(t, err, "a cancelled call leaves the server serving")
				_ = cs.Close()
			}
			select {
			case <-served:
			case <-time.After(10 * time.Second):
				t.Fatal("the server did not exit once the call had unwound")
			}
			svc.AssertExpectations(t)
		})
	}
}
