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
		orphan    = model.Session{ID: demoSession, Name: "orphan", From: &model.SessionOrigin{Session: removedSession}}
		byDefault = &model.CurrentSessionSource{Source: model.SessionRefSourceDefault}
		byPointer = &model.CurrentSessionSource{Source: model.SessionRefSourcePointer}
		// byFlag is --session over no pointer: it overrides the default.
		byFlag = &model.CurrentSessionSource{
			Source:    model.SessionRefSourceFlag,
			Overrides: &model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName},
		}
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
		current         *model.SessionID
		pointerErr      error
		groups          []model.ComposeProjectSavepoints
		// listingErr fails the one listing of the sessions.
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
				CurrentSource:   byDefault,
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			name:         "compose projects by name, savepoints newest first, safety snapshots among them",
			savepointsOf: model.DefaultSession,
			groups:       groups,
			want: model.SessionDetail{
				Session:             model.DefaultSessionRecord,
				CurrentSource:       byDefault,
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
			savepointsOf: otherSession,
			ref:          "feature",
			want: model.SessionDetail{
				Session:         feature,
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			name:         "the session the pointer names",
			savepointsOf: otherSession,
			ref:          "feature",
			current:      &otherSession,
			want: model.SessionDetail{
				Session:         feature,
				CurrentSource:   byPointer,
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			name:         "the session --session names is current",
			savepointsOf: otherSession,
			ref:          "feature",
			currentRef:   "feature",
			want: model.SessionDetail{
				Session:         feature,
				CurrentSource:   byFlag,
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			name:         "a session other than the one --session names",
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
			savepointsOf: demoSession,
			ref:          "orphan",
			want: model.SessionDetail{
				Session:         orphan,
				From:            &model.SessionRef{ID: removedSession},
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			name:         "a --session naming no session marks none and shows the session named",
			ref:          "feature",
			currentRef:   "nowhere",
			savepointsOf: otherSession,
			want: model.SessionDetail{
				Session:         feature,
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			name:       "a keyword in --session is refused",
			ref:        "feature",
			currentRef: model.RefLatest,
			wantErr:    model.ErrSessionRefKeyword,
		},
		{
			name:         "current is the session a command works in when it names none",
			ref:          model.RefCurrent,
			savepointsOf: model.DefaultSession,
			want: model.SessionDetail{
				Session:         model.DefaultSessionRecord,
				CurrentSource:   byDefault,
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			name:         "current follows --session",
			ref:          model.RefCurrent,
			currentRef:   "feature",
			savepointsOf: otherSession,
			want: model.SessionDetail{
				Session:         feature,
				CurrentSource:   byFlag,
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			// The ids order feature last: 7d1e… sorts above 0f9c… and 019e….
			name:         "latest is the session of the greatest id",
			ref:          model.RefLatest,
			savepointsOf: otherSession,
			want: model.SessionDetail{
				Session:         feature,
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			name:         "a pointer naming a removed session marks none",
			ref:          "feature",
			current:      &removedSession,
			savepointsOf: otherSession,
			want: model.SessionDetail{
				Session:         feature,
				ComposeProjects: []model.ComposeProjectSavepoints{},
			},
		},
		{
			name:       "a keyword in --session is refused",
			ref:        "feature",
			currentRef: model.RefCurrent,
			wantErr:    model.ErrSessionRefKeyword,
		},
		{
			name:    "a session that does not exist is refused",
			ref:     "nowhere",
			wantErr: model.ErrSessionNotFound,
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
			listingErr: errListing,
			wantErr:    errListing,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := &mockSessionRepo{}
			sessions := []model.Session{model.DefaultSessionRecord, feature, fork, orphan}
			if tt.listingErr != nil {
				repo.On("ListSessions", mock.Anything).Return([]model.Session(nil), tt.listingErr).Once()
			} else {
				repo.On("ListSessions", mock.Anything).Return(sessions, nil).Once()
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
			req := model.ShowSessionRequest{Session: tt.ref}
			if tt.currentRef != "" {
				req.Current = model.CurrentSessionRef{Ref: tt.currentRef, Source: model.SessionRefSourceFlag}
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
	shown := newShownComposeProjects(groups)

	// Assert
	require.Len(t, shown, 1)
	assert.Equal(t, []model.Savepoint{newer, older}, shown[0].Savepoints)
	assert.Equal(t, []model.Savepoint{older, newer}, groups[0].Savepoints)
}
