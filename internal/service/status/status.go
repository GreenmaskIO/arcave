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
	"errors"
	"fmt"
	"time"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/service/sessionref"
)

// inflightMarkerReader reads the interrupted-rollback marker.
type inflightMarkerReader interface {
	Get(ctx context.Context) (marker model.InflightMarker, found bool, err error)
}

// SessionReader lists an arcave project's committed sessions; the DI root opens one per store.
type SessionReader interface {
	ListSessions(ctx context.Context) ([]model.Session, error)
}

// currentSessionReader reads the current-session pointer.
type currentSessionReader interface {
	Get(ctx context.Context) (id model.SessionID, found bool, err error)
}

// Deps are Service's collaborators.
type Deps struct {
	Observer provider.Observer
	Inflight inflightMarkerReader
	// Current is the current-session pointer.
	Current currentSessionReader
	// OpenArcaveProjectStore opens the arcave project's store when a status needs it: one that does
	// not open makes the status partial, not a failure.
	OpenArcaveProjectStore func(ctx context.Context) (storages.Storager, error)
	// NewSessionReader builds a SessionReader over an arcave project's store, NewProviderSession
	// the provider's session id in it.
	NewSessionReader   func(store storages.Storager) SessionReader
	NewProviderSession func(store storages.Navigator, id model.SessionID) provider.Session
	// ArcaveProject is the one the config names; ComposeProject the one -p names.
	ArcaveProject  model.ArcaveProjectName
	ComposeProject model.DockerComposeProjectName
}

// Service reports what runs for one compose project. It takes no lock, writes nothing, and reads
// the store only to add to what the provider reads.
type Service struct {
	observer               provider.Observer
	inflight               inflightMarkerReader
	current                currentSessionReader
	openArcaveProjectStore func(ctx context.Context) (storages.Storager, error)
	newSessionReader       func(store storages.Storager) SessionReader
	newProviderSession     func(store storages.Navigator, id model.SessionID) provider.Session
	arcaveProject          model.ArcaveProjectName
	composeProject         model.DockerComposeProjectName
}

// New builds a Service from deps.
func New(deps Deps) *Service {
	return &Service{
		observer:               deps.Observer,
		inflight:               deps.Inflight,
		current:                deps.Current,
		openArcaveProjectStore: deps.OpenArcaveProjectStore,
		newSessionReader:       deps.NewSessionReader,
		newProviderSession:     deps.NewProviderSession,
		arcaveProject:          deps.ArcaveProject,
		composeProject:         deps.ComposeProject,
	}
}

// storeView is what the store adds to the daemon's answer.
type storeView struct {
	// session is the session commands work in; nil when it could not be resolved.
	session           *model.SessionRef
	sessionUnreadable *model.StatusUnreadable
	// record says whether the store holds the deployed savepoint, savepoint its record when found.
	record          model.DeploymentRecordState
	savepoint       *model.Savepoint
	storeUnreadable *model.StatusUnreadable
}

// Status reports the workload as the provider reads it and what the store adds. An unreadable
// workload, or a session req names that does not exist, fails it; an unreadable store, current
// session or marker returns it with model.ErrStatusPartial.
func (s *Service) Status(ctx context.Context, req model.StatusRequest) (model.ComposeProjectStatus, error) {
	var (
		checkedAt        = time.Now().UTC()
		observed         provider.ObservedWorkload
		deployment       *model.SavepointDeployment
		marker           *model.InflightMarker
		markerUnreadable *model.StatusUnreadable
		view             storeView
		report           model.WorkloadReport
		unreadable       []model.StatusUnreadable
		err              error
	)
	if observed, err = s.observer.Observe(ctx); err != nil {
		return model.ComposeProjectStatus{}, err
	}
	deployment = observed.Deployment()
	marker, markerUnreadable = s.readInflightMarker(ctx)
	if view, err = s.readStoreView(ctx, req.Session, deployment); err != nil {
		return model.ComposeProjectStatus{}, err
	}
	report = observed.Report(checkedAt, view.savepoint)
	unreadable = newUnreadableList(markerUnreadable, view.sessionUnreadable, view.storeUnreadable)
	status := model.ComposeProjectStatus{
		ComposeProject:   s.composeProject,
		CheckedAt:        checkedAt,
		State:            newStatusState(marker, report),
		Session:          view.session,
		Deployment:       newDeploymentStatus(deployment, view),
		RollbackInFlight: newRollbackInFlight(marker),
		Unreadable:       unreadable,
		Notices: newStatusNotices(statusFacts{
			arcaveProject: s.arcaveProject,
			ref:           req.Session,
			marker:        marker,
			deployment:    deployment,
			view:          view,
			unreadable:    unreadable,
		}, report.Notices),
		Detail: report.Detail,
	}
	if len(unreadable) > 0 {
		return status, model.ErrStatusPartial
	}
	return status, nil
}

// readInflightMarker reads this compose project's inflight marker; another's is not this one's.
func (s *Service) readInflightMarker(ctx context.Context) (*model.InflightMarker, *model.StatusUnreadable) {
	marker, found, err := s.inflight.Get(ctx)
	switch {
	case err != nil:
		return nil, newUnreadable(model.StatusUnreadableInflightMarker, err)
	case !found || marker.ComposeProject != s.composeProject:
		return nil, nil
	default:
		return &marker, nil
	}
}

// readStoreView resolves the session the user works in and reads the deployed savepoint's record.
// A ref naming no session is its error.
func (s *Service) readStoreView(
	ctx context.Context, ref string, deployment *model.SavepointDeployment,
) (storeView, error) {
	store, err := s.openArcaveProjectStore(ctx)
	if err != nil {
		return storeView{
			record:          s.newRecordState(deployment, err, nil),
			storeUnreadable: newUnreadable(model.StatusUnreadableStore, err),
		}, nil
	}
	session, sessionUnreadable, err := s.readResolvedSession(ctx, s.newSessionReader(store), ref)
	if err != nil {
		return storeView{}, err
	}
	savepoint, err := s.readDeployedSavepoint(ctx, store, deployment)
	return storeView{
		session:           session,
		sessionUnreadable: sessionUnreadable,
		record:            s.newRecordState(deployment, err, savepoint),
		savepoint:         savepoint,
		storeUnreadable:   newUnreadable(model.StatusUnreadableStore, err),
	}, nil
}

// readResolvedSession resolves ref as every command does. A ref naming no session, several, or a
// keyword is the user's to fix and the error; any other failure is one thing the status could not read.
func (s *Service) readResolvedSession(
	ctx context.Context, sessions SessionReader, ref string,
) (*model.SessionRef, *model.StatusUnreadable, error) {
	session, err := sessionref.Resolve(ctx, sessions, s.current, ref)
	if err != nil {
		if isSessionRefError(err) {
			return nil, nil, err
		}
		return nil, newUnreadable(model.StatusUnreadableSession, err), nil
	}
	return &model.SessionRef{
		ID:   session.ID,
		Name: session.Name,
	}, nil, nil
}

// isSessionRefError reports a ref naming no session, several, or a keyword.
func isSessionRefError(err error) bool {
	_, ambiguous := errors.AsType[*model.AmbiguousSessionNameError](err)
	return ambiguous || errors.Is(err, model.ErrSessionNotFound) || errors.Is(err, model.ErrSessionRefKeyword)
}

// readDeployedSavepoint reads the deployed savepoint's record in its own session; nil for none, a
// session removed since included, and for another arcave project's, which is not this one's to read.
func (s *Service) readDeployedSavepoint(
	ctx context.Context, store storages.Navigator, deployment *model.SavepointDeployment,
) (*model.Savepoint, error) {
	if !s.readsRecordOf(deployment) {
		return nil, nil
	}
	savepoints, err := s.newProviderSession(store, deployment.Session.ID).
		Savepoints(provider.WorkloadName(s.composeProject))
	if err != nil {
		return nil, fmt.Errorf("open session %s: %w", deployment.Session.ID, err)
	}
	id := deployment.Savepoint.ID
	savepoint, found, err := savepoints.Load(ctx, id)
	switch {
	case err != nil:
		return nil, fmt.Errorf("read savepoint %s of compose project %s: %w", id, s.composeProject, err)
	case !found:
		return nil, nil
	default:
		return &savepoint, nil
	}
}

// newRecordState says whether the store holds the deployed savepoint, off what reading it
// returned; empty for another arcave project's, which is not read.
func (s *Service) newRecordState(
	deployment *model.SavepointDeployment, err error, savepoint *model.Savepoint,
) model.DeploymentRecordState {
	switch {
	case !s.readsRecordOf(deployment):
		return ""
	case err != nil:
		return model.DeploymentRecordUnreadable
	case savepoint == nil:
		return model.DeploymentRecordMissing
	default:
		return model.DeploymentRecordFound
	}
}

// readsRecordOf reports whether the status reads deployment's record: one of this arcave project.
func (s *Service) readsRecordOf(deployment *model.SavepointDeployment) bool {
	return deployment != nil && deployment.ArcaveProject == s.arcaveProject
}

// newUnreadable is what source could not be read for err; nil for none.
func newUnreadable(source model.StatusUnreadableSource, err error) *model.StatusUnreadable {
	if err == nil {
		return nil
	}
	return &model.StatusUnreadable{
		Source: source,
		Error:  err.Error(),
	}
}

// newUnreadableList lists what could not be read, never nil, so none marshals as [], not null.
func newUnreadableList(items ...*model.StatusUnreadable) []model.StatusUnreadable {
	unreadable := []model.StatusUnreadable{}
	for _, item := range items {
		if item != nil {
			unreadable = append(unreadable, *item)
		}
	}
	return unreadable
}

// newRollbackInFlight is the marker as a status reports it.
func newRollbackInFlight(marker *model.InflightMarker) *model.RollbackInFlight {
	if marker == nil {
		return nil
	}
	return &model.RollbackInFlight{
		Phase:   marker.Phase,
		Session: marker.Session,
		Target:  marker.Target,
		Safety:  marker.Safety,
		At:      marker.At,
		Recover: marker.RecoveryID(),
	}
}

// newStatusState is rollback-pending under a marker, else the provider's word.
func newStatusState(marker *model.InflightMarker, report model.WorkloadReport) string {
	if marker != nil {
		return model.StatusStateRollbackPending
	}
	return report.State
}

// newDeploymentStatus is the build the workload carries, and what the deployed savepoint's record
// adds; nil for none.
func newDeploymentStatus(deployment *model.SavepointDeployment, view storeView) *model.DeploymentStatus {
	if deployment == nil {
		return nil
	}
	var description string
	if view.savepoint != nil {
		description = view.savepoint.Description
	}
	return &model.DeploymentStatus{
		DeployedAt:    deployment.DeployedAt,
		ArcaveProject: deployment.ArcaveProject,
		Session:       deployment.Session,
		Savepoint: &model.DeployedSavepointStatus{
			DeployedSavepoint: deployment.Savepoint,
			Description:       description,
		},
		Record: view.record,
	}
}
