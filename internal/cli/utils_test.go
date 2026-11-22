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
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

var (
	errDanglingPointer = fmt.Errorf("%w: 019e0000-0000-7000-8000-000000000002, named by .arcave/current_session",
		model.ErrCurrentSessionNotFound)
	errUnreadablePointer = fmt.Errorf("%w: decoding .arcave/current_session: %w",
		model.ErrCurrentSessionUnreadable, model.ErrInvalidSessionID)
	errCurrentDelete = fmt.Errorf("%w: 019e0000-0000-7000-8000-000000000002", model.ErrCurrentSessionDelete)
	errUnrelated     = errors.New("store unreadable")
)

func TestWithFixHint(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantText string
	}{
		{
			name:     "a dangling pointer",
			err:      errDanglingPointer,
			wantText: errDanglingPointer.Error() + "; run 'arcave session unset' or 'arcave session set <session>'",
		},
		{
			name:     "an unreadable pointer",
			err:      errUnreadablePointer,
			wantText: errUnreadablePointer.Error() + "; run 'arcave session unset' or 'arcave session set <session>'",
		},
		{
			name: "deleting the current session",
			err:  errCurrentDelete,
			wantText: errCurrentDelete.Error() + "; switch with 'arcave session set <session>' or " +
				"'arcave session unset' first, or pass --force to delete it and clear the pointer",
		},
		{name: "an unrelated error", err: errUnrelated, wantText: errUnrelated.Error()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := WithFixHint(tt.err)

			// Assert
			require.ErrorIs(t, got, tt.err)
			assert.Equal(t, tt.wantText, got.Error())
		})
	}
}

func TestWithFixHint_Nil(t *testing.T) {
	// Act & Assert
	assert.NoError(t, WithFixHint(nil))
}

func TestMCPError_CurrentSession(t *testing.T) {
	pin := model.DefaultSession
	tests := []struct {
		name     string
		err      error
		pinned   model.SessionID
		wantText string
	}{
		{name: "a dangling pointer", err: errDanglingPointer,
			wantText: errDanglingPointer.Error() + "; call session_unset or session_set"},
		{name: "an unreadable pointer", err: errUnreadablePointer,
			wantText: errUnreadablePointer.Error() + "; call session_unset or session_set"},
		{name: "deleting the session in use", err: errCurrentDelete,
			wantText: errCurrentDelete.Error() + "; switch with session_set or session_unset first, or pass force " +
				"to delete it anyway"},
		{name: "deleting the session in use on a pinned server", err: errCurrentDelete, pinned: pin,
			wantText: errCurrentDelete.Error() + "; pass force to delete it anyway"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			handler := &MCPHandler{pinnedSession: tt.pinned}

			// Act
			got := handler.mcpError(tt.err)

			// Assert
			require.ErrorIs(t, got, tt.err)
			assert.Equal(t, tt.wantText, got.Error())
			assert.NotContains(t, got.Error(), "arcave session")
		})
	}
}

const (
	hintTarget model.SavepointID = "019e843e-1650-7000-8000-000000000000"
	hintSafety model.SavepointID = "019e844b-7818-7000-8000-000000000000"
)

// newHintMarker is the marker of a rollback to target in phase; safety empty for none.
func newHintMarker(phase model.InflightPhase, target, safety model.SavepointID) model.InflightMarker {
	return model.InflightMarker{
		Phase:          phase,
		Session:        model.DefaultSession,
		ComposeProject: "demo",
		Target:         target,
		Safety:         safety,
	}
}

// The way to recover is the adapter's, each surface in its own spelling, whether the marker
// refuses a workflow or a failure left it standing.
func TestRecoveryError(t *testing.T) {
	session := model.DefaultSession.String()
	tests := []struct {
		name    string
		marker  model.InflightMarker
		stands  bool
		wantCLI string
		wantMCP string
	}{
		{
			name:    "a safety snapshot to go back to",
			marker:  newHintMarker(model.InflightRestoring, hintTarget, hintSafety),
			wantCLI: "recover with `arcave rollback --session " + session + " -p demo " + hintSafety.String() + "`",
			wantMCP: "recover by calling rollback with session " + session + ", compose_project demo and ref " + hintSafety.String(),
		},
		{
			name:    "no safety snapshot",
			marker:  newHintMarker(model.InflightRestoring, hintTarget, ""),
			wantCLI: "recover with `arcave rollback --session " + session + " -p demo " + hintTarget.String() + "`, or name another savepoint",
			wantMCP: "recover by calling rollback with session " + session + ", compose_project demo and ref " +
				hintTarget.String() + ", or another savepoint",
		},
		{
			name:    "a failed recovery accepts any savepoint",
			marker:  newHintMarker(model.InflightRestoring, hintSafety, hintSafety),
			wantCLI: "recover with `arcave rollback --session " + session + " -p demo " + hintSafety.String() + "`, or name another savepoint",
			wantMCP: "recover by calling rollback with session " + session + ", compose_project demo and ref " +
				hintSafety.String() + ", or another savepoint",
		},
		{
			name:   "restore done can be kept instead",
			marker: newHintMarker(model.InflightRestoreDone, hintTarget, hintSafety),
			wantCLI: "recover with `arcave rollback --session " + session + " -p demo " + hintSafety.String() + "`; or keep the " +
				"rollback: start the compose project and remove the marker volume labelled io.arcave.inflight.compose-project=demo",
			wantMCP: "recover by calling rollback with session " + session + ", compose_project demo and ref " + hintSafety.String() +
				"; or keep the rollback: start the compose project and remove the marker volume labelled io.arcave.inflight.compose-project=demo",
		},
		{
			name:   "a failed revert names where its safety snapshot lists",
			marker: newHintMarker(model.InflightInconsistent, hintTarget, hintSafety),
			wantCLI: "recover with `arcave rollback --session " + session + " -p demo " + hintSafety.String() + "`; safety snapshot " +
				hintSafety.String() + " lists under `arcave savepoint list --include-safety-snapshots` and can be rolled back to by id",
			wantMCP: "recover by calling rollback with session " + session + ", compose_project demo and ref " + hintSafety.String() +
				"; savepoint_list with include_safety_snapshots lists safety snapshot " + hintSafety.String() + ", which rollback takes by id",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			err := &model.InterruptedRollbackError{Marker: tt.marker}
			handler := &MCPHandler{}

			// Act
			cli := WithFixHint(err)
			mcp := handler.mcpError(err)

			// Assert
			assert.Equal(t, err.Error()+"; "+tt.wantCLI, cli.Error())
			assert.Equal(t, err.Error()+"; "+tt.wantMCP, mcp.Error())
			assert.NotContains(t, mcp.Error(), "arcave ")
			assert.ErrorIs(t, cli, err)
			assert.ErrorIs(t, mcp, err)
		})
	}
}

// The session refusals a force gets past, and a removal that stopped partway, get their fix from
// the adapter.
func TestSessionFixError(t *testing.T) {
	var (
		cloning     = fmt.Errorf("%w: x's heartbeat is younger than its stale age", model.ErrSessionCloning)
		unknown     = fmt.Errorf("%w: x: unreadable", model.ErrSessionStatusUnknown)
		partial     = fmt.Errorf("deleting session x: boom; %w", model.ErrSessionRemovePartial)
		otherResult = errors.New("boom")
	)
	tests := []struct {
		name    string
		err     error
		wantCLI string
		wantMCP string
	}{
		{
			name:    "the default session",
			err:     model.ErrDefaultSessionDelete,
			wantCLI: "; pass --force to empty it, every savepoint it holds included",
			wantMCP: "; pass force to empty it, every savepoint it holds included",
		},
		{
			name:    "a fork still cloning",
			err:     cloning,
			wantCLI: "; if no fork is running, pass --force to delete it",
			wantMCP: "; if no fork is running, pass force to delete it",
		},
		{
			name:    "a tree of unknown status",
			err:     unknown,
			wantCLI: "; if nothing is writing it, pass --force to delete it",
			wantMCP: "; if nothing is writing it, pass force to delete it",
		},
		{
			name:    "a removal that stopped partway",
			err:     partial,
			wantCLI: "; run it again with the session id to remove the rest",
			wantMCP: "; call session_remove again with the session id to remove the rest",
		},
		{name: "another outcome", err: otherResult},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			cli := WithFixHint(tt.err)
			mcp := (&MCPHandler{}).mcpError(tt.err)

			// Assert
			assert.Equal(t, tt.err.Error()+tt.wantCLI, cli.Error())
			assert.Equal(t, tt.err.Error()+tt.wantMCP, mcp.Error())
		})
	}
}

// A gc refused by an interrupted rollback says a dry run still reports, before the way to recover.
func TestGCInflightError(t *testing.T) {
	// Arrange
	refused := &model.InterruptedRollbackError{Marker: newHintMarker(model.InflightRestoring, hintTarget, hintSafety)}

	// Act
	cli := WithFixHint(gcInflightError(refused, cliGCInflightFixHint))
	mcp := (&MCPHandler{}).mcpError(gcInflightError(refused, mcpGCInflightFixHint))

	// Assert
	assert.Equal(t, refused.Error()+"; `arcave gc --dry-run` still reports; recover with `arcave rollback --session "+
		model.DefaultSession.String()+" -p demo "+hintSafety.String()+"`", cli.Error())
	assert.Contains(t, mcp.Error(), refused.Error()+"; call gc with dry_run, which still reports; recover by calling rollback")
	assert.Equal(t, errUnrelated, gcInflightError(errUnrelated, cliGCInflightFixHint))
}

func TestRemoveUsageError_Unparsable(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantHint string
	}{
		{
			name:     "retain-for",
			err:      fmt.Errorf("%w %q: bad", model.ErrInvalidRetainFor, "2 weeks"),
			wantHint: "; retain-for takes a duration, e.g. 24h or 7d",
		},
		{
			name:     "before-date",
			err:      fmt.Errorf("%w %q: bad", model.ErrInvalidBeforeDate, "june"),
			wantHint: "; before-date takes an RFC 3339 timestamp, e.g. 2026-06-01T00:00:00Z",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := removeUsageError(tt.err)

			// Assert
			require.ErrorIs(t, got, tt.err)
			assert.Equal(t, tt.err.Error()+tt.wantHint, got.Error())
		})
	}
}
