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

package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestSessionService_ShowSession(t *testing.T) {
	removedSession := model.SessionID(uuid.MustParse("019e2222-0000-7000-8000-000000000000"))
	var (
		older  = model.Savepoint{ID: "019e0000-0000-7000-8000-000000000001", Name: "seed", CreatedAt: sessionNow}
		newer  = model.Savepoint{ID: "019e0000-0000-7000-8000-000000000002", CreatedAt: sessionNow.Add(time.Hour)}
		safety = model.Savepoint{
			ID:        "019e0000-0000-7000-8000-000000000003",
			Labels:    map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue},
			CreatedAt: sessionNow.Add(2 * time.Hour),
		}
		feature = sessionRecord(otherSession, "feature", sessionNow)
		fork    = model.Session{ID: thirdSession, Name: "fork", From: &model.SessionOrigin{Session: otherSession}}
		// orphan is a fork whose source has since been removed.
		orphan     = model.Session{ID: demoSession, Name: "orphan", From: &model.SessionOrigin{Session: removedSession}}
		errList    = errors.New("store timeout")
		errListing = errors.New("sessions unreadable")
		// groups are as the repository lists them: compose projects by name, savepoints oldest first.
		groups = []model.ComposeProjectSavepoints{
			{ComposeProject: "blog", Savepoints: []model.Savepoint{older}},
			{ComposeProject: "shop", Savepoints: []model.Savepoint{older, newer, safety}},
			{ComposeProject: "tools", Savepoints: []model.Savepoint{safety}},
		}
	)
	tests := []struct {
		name string
		// ref names the session to show, currentRef the one to mark current.
		ref, currentRef string
		includeSafety   bool
		current         *model.SessionID
		pointerErr      error
		groups          []model.ComposeProjectSavepoints
		// listings is how many times the sessions are listed; listingErr fails the last.
		listings   int
		listingErr error
		// savepointsOf is the session whose savepoints are listed, none when zero; listErr fails it.
		savepointsOf model.SessionID
		listErr      error
		want         model.SessionDetail
		wantErr      error
	}{
		{
			name:         "an empty session",
			savepointsOf: model.DefaultSession,
			want: model.SessionDetail{
				Session:         model.DefaultSessionRecord,
				Current:         true,
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			name:         "compose projects by name, savepoints newest first, safety snapshots hidden",
			savepointsOf: model.DefaultSession,
			groups:       groups,
			want: model.SessionDetail{
				Session: model.DefaultSessionRecord,
				Current: true,
				// Counted whatever is shown.
				SavepointCount:      3,
				SafetySnapshotCount: 2,
				ComposeProjects: []model.ComposeProjectSavepoints{
					{ComposeProject: "blog", Savepoints: []model.Savepoint{older}},
					{ComposeProject: "shop", Savepoints: []model.Savepoint{newer, older}},
				},
			},
		},
		{
			name:          "safety snapshots shown when asked for",
			savepointsOf:  model.DefaultSession,
			groups:        groups,
			includeSafety: true,
			want: model.SessionDetail{
				Session: model.DefaultSessionRecord,
				Current: true,
				// Counted whatever is shown.
				SavepointCount:      3,
				SafetySnapshotCount: 2,
				ComposeProjects: []model.ComposeProjectSavepoints{
					{ComposeProject: "blog", Savepoints: []model.Savepoint{older}},
					{ComposeProject: "shop", Savepoints: []model.Savepoint{safety, newer, older}},
					{ComposeProject: "tools", Savepoints: []model.Savepoint{safety}},
				},
			},
		},
		{
			name:         "a session other than the current one",
			listings:     1,
			savepointsOf: otherSession,
			ref:          "feature",
			want: model.SessionDetail{
				Session:         feature,
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			name:         "the session the pointer names",
			listings:     1,
			savepointsOf: otherSession,
			ref:          "feature",
			current:      &otherSession,
			want: model.SessionDetail{
				Session:         feature,
				Current:         true,
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			name:         "the session --session names is current, listed once",
			listings:     1,
			savepointsOf: otherSession,
			ref:          "feature",
			currentRef:   "feature",
			want: model.SessionDetail{
				Session:         feature,
				Current:         true,
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			name:         "a session other than the one --session names",
			listings:     1,
			savepointsOf: otherSession,
			ref:          "feature",
			currentRef:   model.DefaultSessionName,
			want: model.SessionDetail{
				Session:         feature,
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			name:         "a pointer that cannot be read marks none",
			listings:     1,
			savepointsOf: model.DefaultSession,
			ref:          model.DefaultSessionName,
			pointerErr:   errors.New("permission denied"),
			want: model.SessionDetail{
				Session:         model.DefaultSessionRecord,
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			name:         "a fork names the session it was forked from",
			listings:     1,
			savepointsOf: thirdSession,
			ref:          "fork",
			want: model.SessionDetail{
				Session:         fork,
				From:            &model.SessionRef{ID: otherSession, Name: "feature"},
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			name:         "a fork of a removed session names its id alone",
			listings:     1,
			savepointsOf: demoSession,
			ref:          "orphan",
			want: model.SessionDetail{
				Session:         orphan,
				From:            &model.SessionRef{ID: removedSession},
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			name:       "a --session naming no session is refused",
			listings:   1,
			ref:        "feature",
			currentRef: "nowhere",
			wantErr:    model.ErrSessionNotFound,
		},
		{
			name:     "a session that does not exist is refused",
			listings: 1,
			ref:      "nowhere",
			wantErr:  model.ErrSessionNotFound,
		},
		{
			name:         "savepoints that cannot be listed are refused",
			savepointsOf: model.DefaultSession,
			listErr:      errList,
			wantErr:      errList,
		},
		{
			name:       "sessions that cannot be listed are refused",
			ref:        "fork",
			listings:   1,
			listingErr: errListing,
			wantErr:    errListing,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := &mockSessionRepo{}
			sessions := []model.Session{model.DefaultSessionRecord, feature, fork, orphan}
			for i := range tt.listings {
				var listingErr error
				if i == tt.listings-1 {
					listingErr = tt.listingErr
				}
				if listingErr != nil {
					repo.On("ListSessions", mock.Anything).Return([]model.Session(nil), listingErr).Once()
				} else {
					repo.On("ListSessions", mock.Anything).Return(sessions, nil).Once()
				}
			}
			if tt.savepointsOf != (model.SessionID{}) {
				repo.On("ListSessionSavepoints", mock.Anything, tt.savepointsOf).Return(tt.groups, tt.listErr).Once()
			}
			deps := SessionDeps{
				NewSessionLocker: newFakeSessionLocker,
				Current:          &fakePointer{id: tt.current, err: tt.pointerErr},
				Sessions:         repo,
			}
			svc := NewSessionService(deps)
			svc.sessionSavepoints = repo.ListSessionSavepoints
			req := model.ShowSessionRequest{
				Session:                tt.ref,
				Ref:                    tt.currentRef,
				IncludeSafetySnapshots: tt.includeSafety,
			}

			// Act
			got, err := svc.ShowSession(context.Background(), req)

			// Assert
			assert.ErrorIs(t, err, tt.wantErr)
			if tt.wantErr == nil {
				assert.False(t, got.CheckedAt.IsZero())
				got.CheckedAt = time.Time{}
			}
			assert.Equal(t, tt.want, got)
			repo.AssertExpectations(t)
		})
	}
}

// The listing's own groups are left as the repository returned them.
func TestNewShownComposeProjects_LeavesItsInputAlone(t *testing.T) {
	// Arrange
	older := model.Savepoint{ID: "019e0000-0000-7000-8000-000000000001"}
	newer := model.Savepoint{ID: "019e0000-0000-7000-8000-000000000002"}
	groups := []model.ComposeProjectSavepoints{{ComposeProject: "shop", Savepoints: []model.Savepoint{older, newer}}}

	// Act
	shown := newShownComposeProjects(groups, true)

	// Assert
	require.Len(t, shown, 1)
	assert.Equal(t, []model.Savepoint{newer, older}, shown[0].Savepoints)
	assert.Equal(t, []model.Savepoint{older, newer}, groups[0].Savepoints)
}
