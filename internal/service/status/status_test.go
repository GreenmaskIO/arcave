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

package status

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	sessionrepo "github.com/greenmaskio/arcave/internal/repository/session"
)

var (
	statusCheckedAt = time.Date(2026, 11, 7, 17, 14, 0, 0, time.UTC)
	statusFeat      = model.Session{ID: model.SessionID{0x01, 0x9a, 0x70, 0, 0, 0, 0x70, 0, 0x80, 0, 0, 0, 0, 0, 0, 0x01}, Name: "feat"}
	statusFeatTwin  = model.Session{ID: model.SessionID{0x01, 0x9a, 0x70, 0, 0, 0, 0x70, 0, 0x80, 0, 0, 0, 0, 0, 0, 0x02}, Name: "feat"}
	statusDeployed  = model.Savepoint{
		ID:          "019e843e-1650-7000-8000-000000000000",
		Name:        "before-migration",
		Description: "the schema before 0042",
		CreatedAt:   time.Date(2026, 11, 6, 19, 41, 0, 0, time.UTC),
	}
	statusNewer = model.Savepoint{
		ID:        "019e9000-0000-7000-8000-000000000000",
		Name:      "after-fix",
		CreatedAt: time.Date(2026, 11, 7, 16, 14, 0, 0, time.UTC),
	}
	// demoReport is what the provider says of a running workload.
	demoReport = model.WorkloadReport{
		State:   "running",
		Notices: []model.Notice{{Code: "replica-restarted", Message: "db-1 restarted 2 times since it was created"}},
		Detail:  fakeDetail{body: `{"services":[]}`},
	}
)

func TestService_Status(t *testing.T) {
	now := time.Now().UTC()
	baselineDeployment := newStatusDeployment(statusDeployed, model.DefaultSessionRecord)
	featDeployment := newStatusDeployment(statusDeployed, statusFeat)
	otherProjectDeployment := newStatusDeployment(statusDeployed, statusFeat)
	otherProjectDeployment.ArcaveProject = "other-checkout"
	safety := model.Savepoint{
		ID:     "019e8500-0000-7000-8000-000000000000",
		Labels: map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue}, CreatedAt: statusDeployed.CreatedAt.Add(time.Hour),
	}
	safetyDeployment := newStatusDeployment(safety, model.DefaultSessionRecord)
	tests := []struct {
		name string
		// deployment is the build the workload carries; nil for none.
		deployment *model.SavepointDeployment
		// report is what the provider says; demoReport when zero.
		report model.WorkloadReport
		// wantRecord is the record handed to Report: nil when arcave read none.
		wantRecord *model.Savepoint
		// stored are the savepoints of the default session's compose project.
		stored   []model.Savepoint
		sessions []model.Session
		pointer  *model.SessionID
		// pointerErr is the current-session pointer's read failing.
		pointerErr error
		ref        string
		marker     *model.InflightMarker
		// sessionOpenErr is opening the deployed session's savepoints failing.
		sessionOpenErr error
		// markerErr is the inflight marker's read failing.
		markerErr error
		storeErr  error
		// sessionsErr and savepointsErr fail every read of sessions, of savepoints.
		sessionsErr   error
		savepointsErr error
		verify        func(t *testing.T, status model.ComposeProjectStatus)
		wantErr       error
		// wantAmbiguous expects the error to name a session name several sessions carry.
		wantAmbiguous bool
	}{
		{
			name:       "a rollback deployed the workload and the store holds it",
			deployment: &baselineDeployment,
			stored:     []model.Savepoint{statusDeployed, statusNewer},
			wantRecord: &statusDeployed,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, "running", status.State, "the provider's word")
				assert.False(t, status.CheckedAt.Before(now), "checked when it runs")
				assert.Equal(t, model.DockerComposeProjectName(demoProject), status.ComposeProject)
				assert.Equal(t, &model.DeploymentStatus{
					DeployedAt:    baselineDeployment.DeployedAt,
					ArcaveProject: demoArcaveProject,
					Session:       model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName},
					Savepoint: &model.DeployedSavepointStatus{
						DeployedSavepoint: baselineDeployment.Savepoint,
						Description:       statusDeployed.Description,
					},
					Record: model.DeploymentRecordFound,
				}, status.Deployment, "the stamp says what was deployed, the record adds its description")
				assert.Equal(t, &model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName}, status.Session)
				assert.Equal(t, demoReport.Notices, status.Notices, "the provider's notices as they are")
				assert.Equal(t, demoReport.Detail, status.Detail, "the provider's body as it is")
			},
		},
		{
			name:       "arcave's notices and the provider's together, the severe first",
			deployment: &baselineDeployment,
			stored:     []model.Savepoint{statusDeployed},
			wantRecord: &statusDeployed,
			pointer:    &statusFeat.ID,
			sessions:   []model.Session{statusFeat},
			report: model.WorkloadReport{
				State:   "degraded",
				Notices: []model.Notice{{Code: "replica-exited", Message: "db-1 exited with code 1", Severe: true}},
				Detail:  demoReport.Detail,
			},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, "degraded", status.State)
				assert.Equal(t, []model.NoticeCode{"replica-exited", model.NoticeCodeOtherSession},
					noticeCodes(status.Notices))
			},
		},
		{
			name:       "deployed from one session, commands working in another",
			deployment: &baselineDeployment,
			stored:     []model.Savepoint{statusDeployed},
			wantRecord: &statusDeployed,
			pointer:    &statusFeat.ID,
			sessions:   []model.Session{statusFeat},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, &model.SessionRef{ID: statusFeat.ID, Name: statusFeat.Name}, status.Session)
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.DeploymentRecordFound, status.Deployment.Record, "the deployed session is read")
				assertNotice(t, status.Notices, model.NoticeCodeOtherSession, "running savepoint before-migration "+
					"(019e843e-1650-7000-8000-000000000000) of session baseline; arcave commands act on session feat")
			},
		},
		{
			name:       "deployed by another arcave project: its store is not read, and nothing reads gone",
			deployment: &otherProjectDeployment,
			stored:     []model.Savepoint{statusNewer},
			report:     model.WorkloadReport{State: "running", Detail: demoReport.Detail},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.ArcaveProjectName("other-checkout"), status.Deployment.ArcaveProject)
				assert.Empty(t, status.Deployment.Record, "another arcave project's store is not read")
				assert.Equal(t, []model.Notice{{
					Code: model.NoticeCodeOtherArcaveProject,
					Message: "running savepoint before-migration (019e843e-1650-7000-8000-000000000000) of arcave project " +
						"other-checkout; arcave commands act on arcave project demo-arcave",
				}}, status.Notices, "no deployed-savepoint-gone, and no other-session: the session is another project's")
			},
		},
		{
			name:       "deployed from a session since removed",
			deployment: &featDeployment,
			stored:     []model.Savepoint{statusDeployed},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.DeploymentRecordMissing, status.Deployment.Record)
				assert.Equal(t, statusDeployed.Name, status.Deployment.Savepoint.Name, "the stamp names it with no record")
				assert.Empty(t, status.Deployment.Savepoint.Description)
				assertNotice(t, status.Notices, model.NoticeCodeDeployedSavepointGone,
					"savepoint before-migration (019e843e-1650-7000-8000-000000000000) is no longer in session feat")
				assertNotice(t, status.Notices, model.NoticeCodeOtherSession, "running savepoint before-migration "+
					"(019e843e-1650-7000-8000-000000000000) of session feat; arcave commands act on session baseline")
			},
		},
		{
			name:       "a deployed savepoint removed since",
			deployment: &baselineDeployment,
			stored:     []model.Savepoint{statusNewer},
			report:     model.WorkloadReport{State: "running", Detail: demoReport.Detail},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.DeploymentRecordMissing, status.Deployment.Record)
				assert.Equal(t, []model.Notice{{
					Code:    model.NoticeCodeDeployedSavepointGone,
					Message: "savepoint before-migration (019e843e-1650-7000-8000-000000000000) is no longer in session baseline",
				}}, status.Notices)
			},
		},
		{
			name:       "a rollback in flight outranks the provider's word",
			deployment: &baselineDeployment,
			stored:     []model.Savepoint{statusDeployed},
			wantRecord: &statusDeployed,
			report:     model.WorkloadReport{State: "degraded", Detail: demoReport.Detail},
			marker: &model.InflightMarker{Phase: model.InflightRestoring, Session: model.DefaultSession,
				ComposeProject: demoProject, Target: statusDeployed.ID, Safety: safety.ID, At: statusCheckedAt.Add(-time.Minute)},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, model.StatusStateRollbackPending, status.State)
				require.NotNil(t, status.RollbackInFlight)
				assert.Equal(t, safety.ID, status.RollbackInFlight.Recover)
				require.NotEmpty(t, status.Notices)
				assert.Equal(t, model.Notice{
					Code:    model.NoticeCodeRollbackPending,
					Message: "a rollback to 019e843e-1650-7000-8000-000000000000 is in progress or was interrupted while restoring the volumes",
					Severe:  true,
				}, status.Notices[0])
			},
		},
		{
			name:       "another compose project's marker is not this one's",
			deployment: &baselineDeployment,
			stored:     []model.Savepoint{statusDeployed},
			wantRecord: &statusDeployed,
			marker: &model.InflightMarker{Phase: model.InflightRestoring, Session: model.DefaultSession,
				ComposeProject: "other", Target: statusDeployed.ID},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, "running", status.State)
				assert.Nil(t, status.RollbackInFlight)
			},
		},
		{
			name:   "nothing deployed",
			stored: []model.Savepoint{statusDeployed},
			report: model.WorkloadReport{State: "absent", Detail: demoReport.Detail},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, "absent", status.State)
				assert.Nil(t, status.Deployment)
				assert.Equal(t, &model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName}, status.Session)
				encoded, err := json.Marshal(status)
				require.NoError(t, err)
				assert.Contains(t, string(encoded), `"unreadable":[]`, "an empty list is [], never null")
				assert.Contains(t, string(encoded), `"notices":[]`)
				assert.Contains(t, string(encoded), `"detail":{"services":[]}`, "the provider's body as it encodes it")
			},
		},
		{
			name:       "a store that does not open leaves the provider's part whole",
			deployment: &baselineDeployment,
			storeErr:   model.ErrWrongStoreKey,
			wantErr:    model.ErrStatusPartial,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, "running", status.State)
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.DeploymentRecordUnreadable, status.Deployment.Record)
				assert.Equal(t, statusDeployed.Name, status.Deployment.Savepoint.Name, "the stamp names it with no store")
				assert.Nil(t, status.Session)
				require.Len(t, status.Unreadable, 1)
				assert.Equal(t, model.StatusUnreadableStore, status.Unreadable[0].Source)
				assertNotice(t, status.Notices, model.NoticeCodeStoreUnreadable,
					"the store could not be read: "+model.ErrWrongStoreKey.Error()+"; showing the daemon alone")
				assert.Equal(t, demoReport.Detail, status.Detail)
			},
		},
		{
			name:       "a store that does not open reads no record of another arcave project",
			deployment: &otherProjectDeployment,
			storeErr:   model.ErrWrongStoreKey,
			wantErr:    model.ErrStatusPartial,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				require.NotNil(t, status.Deployment)
				assert.Empty(t, status.Deployment.Record, "another arcave project's record is not read, store or not")
			},
		},
		{
			name:       "a safety snapshot deployed",
			deployment: &safetyDeployment,
			stored:     []model.Savepoint{statusDeployed, safety},
			wantRecord: &safety,
			report:     model.WorkloadReport{State: "running", Detail: demoReport.Detail},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				require.NotNil(t, status.Deployment)
				assert.True(t, status.Deployment.Savepoint.IsSafetySnapshot)
				assert.Equal(t, []model.Notice{{
					Code:    model.NoticeCodeSafetySnapshotDeployed,
					Message: "running 019e8500-0000-7000-8000-000000000000, the safety snapshot of a rollback",
				}}, status.Notices)
			},
		},
		{
			name:       "a session named on the request",
			deployment: &baselineDeployment,
			stored:     []model.Savepoint{statusDeployed},
			wantRecord: &statusDeployed,
			sessions:   []model.Session{statusFeat},
			ref:        "feat",
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, &model.SessionRef{ID: statusFeat.ID, Name: statusFeat.Name}, status.Session)
			},
		},
		{
			name:       "a session named by id on the request",
			deployment: &baselineDeployment,
			stored:     []model.Savepoint{statusDeployed},
			wantRecord: &statusDeployed,
			sessions:   []model.Session{statusFeat},
			ref:        statusFeat.ID.String(),
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, &model.SessionRef{ID: statusFeat.ID, Name: statusFeat.Name}, status.Session)
			},
		},
		{
			name:       "a session ref outranks the pointer",
			deployment: &baselineDeployment,
			stored:     []model.Savepoint{statusDeployed},
			wantRecord: &statusDeployed,
			sessions:   []model.Session{statusFeat},
			pointer:    &statusFeat.ID,
			ref:        model.DefaultSessionName,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, &model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName}, status.Session)
			},
		},
		{
			name:       "a session name two sessions carry is the user's error",
			deployment: &baselineDeployment,
			stored:     []model.Savepoint{statusDeployed},
			sessions:   []model.Session{statusFeat, statusFeatTwin},
			ref:        statusFeat.Name,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, model.ComposeProjectStatus{}, status)
			},
			wantAmbiguous: true,
		},
		{
			name:       "a pointer that cannot be read is partial",
			deployment: &baselineDeployment,
			stored:     []model.Savepoint{statusDeployed},
			wantRecord: &statusDeployed,
			pointerErr: errors.New("current_session: not a session id"),
			wantErr:    model.ErrStatusPartial,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Nil(t, status.Session)
				assert.Equal(t, []model.StatusUnreadable{{
					Source: model.StatusUnreadableSession,
					Error:  "current_session: not a session id",
				}}, status.Unreadable)
			},
		},
		{
			name:       "a session ref that names none is the user's error",
			deployment: &baselineDeployment,
			stored:     []model.Savepoint{statusDeployed},
			ref:        "nope",
			wantErr:    model.ErrSessionNotFound,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, model.ComposeProjectStatus{}, status)
			},
		},
		{
			name:       "a keyword session ref is the user's error",
			deployment: &baselineDeployment,
			stored:     []model.Savepoint{statusDeployed},
			ref:        model.RefCurrent,
			wantErr:    model.ErrSessionRefKeyword,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, model.ComposeProjectStatus{}, status)
			},
		},
		{
			name:       "a pointer to a session since removed is partial",
			deployment: &baselineDeployment,
			stored:     []model.Savepoint{statusDeployed},
			wantRecord: &statusDeployed,
			pointer:    &statusFeat.ID,
			wantErr:    model.ErrStatusPartial,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Nil(t, status.Session)
				require.Len(t, status.Unreadable, 1)
				assert.Equal(t, model.StatusUnreadableSession, status.Unreadable[0].Source)
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.DeploymentRecordFound, status.Deployment.Record, "the deployed session is still read")
				assertNotice(t, status.Notices, model.NoticeCodeSessionUnresolved,
					"the current session could not be resolved: "+status.Unreadable[0].Error)
			},
		},
		{
			name:        "sessions that cannot be listed leave the session unresolved and the record read",
			deployment:  &baselineDeployment,
			stored:      []model.Savepoint{statusDeployed},
			wantRecord:  &statusDeployed,
			pointer:     &statusFeat.ID,
			sessionsErr: errors.New("list failed"),
			wantErr:     model.ErrStatusPartial,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Nil(t, status.Session)
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.DeploymentRecordFound, status.Deployment.Record)
				assert.Equal(t, []model.StatusUnreadable{{
					Source: model.StatusUnreadableSession,
					Error:  "listing sessions: list failed",
				}}, status.Unreadable)
				assertNotice(t, status.Notices, model.NoticeCodeSessionUnresolved,
					"the current session could not be resolved: listing sessions: list failed")
			},
		},
		{
			name:        "a session named on the request whose sessions cannot be listed names it",
			deployment:  &baselineDeployment,
			stored:      []model.Savepoint{statusDeployed},
			wantRecord:  &statusDeployed,
			ref:         "feat",
			sessionsErr: errors.New("list failed"),
			wantErr:     model.ErrStatusPartial,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Nil(t, status.Session)
				assertNotice(t, status.Notices, model.NoticeCodeSessionUnresolved,
					`session "feat" could not be resolved: listing sessions: list failed`)
			},
		},
		{
			name:           "a session whose savepoints cannot be opened is unreadable",
			deployment:     &baselineDeployment,
			sessionOpenErr: errors.New("open failed"),
			wantErr:        model.ErrStatusPartial,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.DeploymentRecordUnreadable, status.Deployment.Record)
				assert.Equal(t, []model.StatusUnreadable{{
					Source: model.StatusUnreadableStore,
					Error:  "open session " + model.DefaultSession.String() + ": open failed",
				}}, status.Unreadable)
			},
		},
		{
			name:          "a record that cannot be read is unreadable",
			deployment:    &baselineDeployment,
			savepointsErr: errors.New("read failed"),
			wantErr:       model.ErrStatusPartial,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.DeploymentRecordUnreadable, status.Deployment.Record)
				assert.Empty(t, status.Deployment.Savepoint.Description)
				assert.Equal(t, []model.StatusUnreadable{{
					Source: model.StatusUnreadableStore,
					Error:  "read savepoint 019e843e-1650-7000-8000-000000000000 of compose project demo: read failed",
				}}, status.Unreadable)
			},
		},
		{
			name:       "a marker that cannot be read is partial",
			deployment: &baselineDeployment,
			stored:     []model.Savepoint{statusDeployed},
			wantRecord: &statusDeployed,
			markerErr:  errors.New("inflight.json: unexpected end of JSON input"),
			wantErr:    model.ErrStatusPartial,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, "running", status.State)
				require.Len(t, status.Unreadable, 1)
				assert.Equal(t, model.StatusUnreadableInflightMarker, status.Unreadable[0].Source)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := zerolog.Nop().WithContext(context.Background())
			store := newDirStore(t)
			seedStatusSessions(t, store, tt.sessions)
			report := tt.report
			if report.State == "" {
				report = demoReport
			}
			var reportedAt time.Time
			observed := &mockObservedWorkload{}
			observed.On("Deployment").Return(tt.deployment).Once()
			reports := !tt.wantAmbiguous && (tt.wantErr == nil || errors.Is(tt.wantErr, model.ErrStatusPartial))
			if reports {
				observed.On("Report", mock.AnythingOfType("time.Time"), tt.wantRecord).Return(report).Once().
					Run(func(args mock.Arguments) { reportedAt = args.Get(0).(time.Time) })
			}
			observer := &mockObserver{}
			observer.On("Observe", mock.Anything).Return(observed, nil).Once()
			marker := &mockInflight{}
			if tt.marker != nil {
				marker.On("Get", mock.Anything).Return(*tt.marker, true, nil).Once()
			} else {
				marker.On("Get", mock.Anything).Return(nil, false, tt.markerErr).Once()
			}
			svc := New(Deps{
				Observer: observer,
				Inflight: marker,
				Current:  &fakePointer{id: tt.pointer, err: tt.pointerErr},
				OpenArcaveProjectStore: func(context.Context) (storages.Storager, error) {
					if tt.storeErr != nil {
						return nil, tt.storeErr
					}
					return store, nil
				},
				NewSessionReader: func(store storages.Storager) SessionReader {
					if tt.sessionsErr != nil {
						return failingReader{err: tt.sessionsErr}
					}
					return sessionrepo.New(store)
				},
				NewProviderSession: func(arcaveProjectStore storages.Navigator, id model.SessionID) provider.Session {
					assert.Same(t, store, arcaveProjectStore, "the session lies in the arcave project's store")
					return newFakeSession(id, tt.stored, tt.sessionOpenErr, tt.savepointsErr)
				},
				ArcaveProject:  demoArcaveProject,
				ComposeProject: demoProject,
			})

			// Act
			status, err := svc.Status(ctx, model.StatusRequest{Session: tt.ref})

			// Assert
			var ambiguous *model.AmbiguousSessionNameError
			switch {
			case tt.wantAmbiguous:
				require.ErrorAs(t, err, &ambiguous)
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			default:
				require.NoError(t, err)
			}
			tt.verify(t, status)
			if reports {
				assert.Equal(t, status.CheckedAt, reportedAt, "the provider counts its ages from the status's own time")
			}
			observer.AssertExpectations(t)
			observed.AssertExpectations(t)
			marker.AssertExpectations(t)
		})
	}
}

func TestService_Status_ReadsTheDeploymentThenTheRecordThenReports(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	store := newDirStore(t)
	deployment := newStatusDeployment(statusDeployed, model.DefaultSessionRecord)
	observed := &mockObservedWorkload{}
	observer := &mockObserver{}
	savepoints := &mockSavepoints{}
	marker := &mockInflight{}
	marker.On("Get", mock.Anything).Return(nil, false, nil).Once()
	mock.InOrder(
		observer.On("Observe", mock.Anything).Return(observed, nil).Once(),
		observed.On("Deployment").Return(&deployment).Once(),
		savepoints.On("Load", mock.Anything, statusDeployed.ID).Return(statusDeployed, true, nil).Once(),
		observed.On("Report", mock.AnythingOfType("time.Time"), &statusDeployed).Return(demoReport).Once(),
	)
	svc := New(Deps{
		Observer:               observer,
		Inflight:               marker,
		Current:                &fakePointer{},
		OpenArcaveProjectStore: func(context.Context) (storages.Storager, error) { return store, nil },
		NewSessionReader:       func(store storages.Storager) SessionReader { return sessionrepo.New(store) },
		NewProviderSession: func(_ storages.Navigator, id model.SessionID) provider.Session {
			return sessionOf{
				fakeSession: newFakeSession(id, nil, nil, nil),
				savepoints:  savepoints,
			}
		},
		ArcaveProject:  demoArcaveProject,
		ComposeProject: demoProject,
	})

	// Act
	status, err := svc.Status(ctx, model.StatusRequest{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "running", status.State)
	observer.AssertExpectations(t)
	observed.AssertExpectations(t)
	savepoints.AssertExpectations(t)
	marker.AssertExpectations(t)
}

func TestService_Status_ADaemonThatCannotBeReadFailsIt(t *testing.T) {
	// Arrange: nothing past the daemon is read, so nothing else is expected.
	observer := &mockObserver{}
	observer.On("Observe", mock.Anything).Return(nil, errors.New("daemon unreachable")).Once()
	svc := New(Deps{
		Observer:       observer,
		ComposeProject: demoProject,
	})

	// Act
	status, err := svc.Status(zerolog.Nop().WithContext(context.Background()), model.StatusRequest{})

	// Assert
	require.ErrorContains(t, err, "daemon unreachable")
	assert.Equal(t, model.ComposeProjectStatus{}, status)
	observer.AssertExpectations(t)
}

// newStatusDeployment is a rollback to savepoint from session.
func newStatusDeployment(savepoint model.Savepoint, session model.Session) model.SavepointDeployment {
	return model.SavepointDeployment{
		DeployedAt:    time.Date(2026, 11, 7, 14, 2, 0, 0, time.UTC),
		ArcaveProject: demoArcaveProject,
		Session:       model.SessionRef{ID: session.ID, Name: session.Name},
		Savepoint: model.DeployedSavepoint{
			ID:               savepoint.ID,
			Name:             savepoint.Name,
			CreatedAt:        savepoint.CreatedAt,
			IsSafetySnapshot: savepoint.IsSafetySnapshot(),
		},
	}
}

// seedStatusSessions commits sessions.
func seedStatusSessions(t *testing.T, store storages.Storager, sessions []model.Session) {
	t.Helper()
	for _, session := range sessions {
		require.NoError(t, sessionrepo.New(store).SaveSession(context.Background(), session))
	}
}

// assertNotice asserts a notice of code carries message.
func assertNotice(t *testing.T, notices []model.Notice, code model.NoticeCode, message string) {
	t.Helper()
	assert.Contains(t, notices, model.Notice{Code: code, Message: message}, "no %s notice %q", code, message)
}

// noticeCodes are the notices' codes in their order.
func noticeCodes(notices []model.Notice) []model.NoticeCode {
	codes := make([]model.NoticeCode, 0, len(notices))
	for _, notice := range notices {
		codes = append(codes, notice.Code)
	}
	return codes
}

func TestNewInflightNotice(t *testing.T) {
	tests := []struct {
		phase model.InflightPhase
		want  string
	}{
		{phase: model.InflightRecreating, want: "while recreating the containers"},
		{phase: model.InflightRestoring, want: "while restoring the volumes"},
		{phase: model.InflightRestoreDone, want: "while starting the services"},
		{phase: model.InflightInconsistent, want: "and its revert failed"},
		{phase: "unknown-phase", want: `in phase "unknown-phase"`},
	}
	for _, tt := range tests {
		t.Run(string(tt.phase), func(t *testing.T) {
			// Arrange
			marker := model.InflightMarker{Phase: tt.phase, Target: statusDeployed.ID}

			// Act
			notice := newInflightNotice(marker)

			// Assert
			assert.Equal(t, model.Notice{
				Code:    model.NoticeCodeRollbackPending,
				Message: "a rollback to 019e843e-1650-7000-8000-000000000000 is in progress or was interrupted " + tt.want,
				Severe:  true,
			}, notice)
		})
	}
}

func TestNewRollbackInFlight_Recover(t *testing.T) {
	tests := []struct {
		name   string
		safety model.SavepointID
		want   model.SavepointID
	}{
		{name: "the safety snapshot recovers a rollback that took one", safety: statusNewer.ID, want: statusNewer.ID},
		{name: "the target recovers a rollback that took none", want: statusDeployed.ID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			marker := &model.InflightMarker{Phase: model.InflightRestoring, Target: statusDeployed.ID, Safety: tt.safety}

			// Act
			inFlight := newRollbackInFlight(marker)

			// Assert
			require.NotNil(t, inFlight)
			assert.Equal(t, tt.want, inFlight.Recover)
		})
	}
}
