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
	"slices"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

// statusReadAttempts bounds the reads of a compose project a rollback is rebuilding: a container
// listed and gone before its inspect is read again, not reported.
const statusReadAttempts = 3

// errComposeProjectChanging refuses a status of a compose project whose containers kept changing
// under the read.
var errComposeProjectChanging = errors.New("the compose project changed while status read it; run it again")

// composeContainersReader reads a compose project's containers alone.
type composeContainersReader interface {
	ReadContainers(ctx context.Context) (model.ComposeProjectContainers, error)
}

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

// SavepointLoader reads the records of a compose project's savepoints by id; the DI root opens one
// per store.
type SavepointLoader interface {
	LoadSavepoints(ctx context.Context, ids []model.SavepointID) ([]model.Savepoint, error)
}

// Deps are Service's collaborators.
type Deps struct {
	ComposeContainers composeContainersReader
	Inflight          inflightMarkerReader
	// Current is the current-session pointer.
	Current currentSessionReader
	// OpenArcaveProjectStore opens the arcave project's store when a status needs it: one that does
	// not open makes the status partial, not a failure.
	OpenArcaveProjectStore func(ctx context.Context) (storages.Storager, error)
	// NewSessionReader builds a SessionReader over an arcave project's store, NewSavepointLoader a
	// SavepointLoader over a compose project's.
	NewSessionReader   func(store storages.Storager) SessionReader
	NewSavepointLoader func(store storages.Storager) SavepointLoader
	// ArcaveProject is the one the config names; ComposeProject the one -p names.
	ArcaveProject  model.ArcaveProjectName
	ComposeProject model.DockerComposeProjectName
}

// Service reports what the daemon runs for one compose project. It takes no lock, writes
// nothing, and reads the store only to add to what the daemon's labels say.
type Service struct {
	composeContainers      composeContainersReader
	inflight               inflightMarkerReader
	current                currentSessionReader
	openArcaveProjectStore func(ctx context.Context) (storages.Storager, error)
	newSessionReader       func(store storages.Storager) SessionReader
	newSavepointLoader     func(store storages.Storager) SavepointLoader
	arcaveProject          model.ArcaveProjectName
	composeProject         model.DockerComposeProjectName
}

// New builds a Service from deps.
func New(deps Deps) *Service {
	return &Service{
		composeContainers:      deps.ComposeContainers,
		inflight:               deps.Inflight,
		current:                deps.Current,
		openArcaveProjectStore: deps.OpenArcaveProjectStore,
		newSessionReader:       deps.NewSessionReader,
		newSavepointLoader:     deps.NewSavepointLoader,
		arcaveProject:          deps.ArcaveProject,
		composeProject:         deps.ComposeProject,
	}
}

// storeView is what the store adds to the daemon's answer.
type storeView struct {
	session *model.SessionRef
	deployedRecord
}

// deployedRecord is whether the store holds the deployed savepoint, and its record when it does.
type deployedRecord struct {
	record model.DeploymentRecord
	// savepoint is the deployed savepoint's record; nil unless record is found.
	savepoint  *model.Savepoint
	unreadable []model.StatusUnreadable
}

// Status reports the compose project as the daemon runs it and what the store adds. An unreadable
// daemon, or a session req names that does not exist, fails it; an unreadable store, current session
// or marker returns it with model.ErrStatusPartial.
func (s *Service) Status(ctx context.Context, req model.StatusRequest) (model.ComposeProjectStatus, error) {
	var (
		checkedAt        = time.Now().UTC()
		containers       model.ComposeProjectContainers
		deployed         deployedContainers
		services         []model.ServiceStatus
		marker           *model.InflightMarker
		markerUnreadable []model.StatusUnreadable
		view             storeView
		unreadable       []model.StatusUnreadable
		state            model.ComposeProjectState
		err              error
	)
	if containers, err = s.readContainers(ctx); err != nil {
		return model.ComposeProjectStatus{}, err
	}
	deployed = findDeployments(containers)
	services = newServiceStatuses(containers, deployed)
	marker, markerUnreadable = s.readInflightMarker(ctx)
	if view, err = s.readStoreView(ctx, req.Session, deployed.deployment); err != nil {
		return model.ComposeProjectStatus{}, err
	}
	// Non-nil, so none marshals as [], not null: slices.Concat of empty slices is nil.
	unreadable = append(append([]model.StatusUnreadable{}, markerUnreadable...), view.unreadable...)
	state = newComposeProjectState(marker != nil, services)
	status := model.ComposeProjectStatus{
		ComposeProject:   s.composeProject,
		CheckedAt:        checkedAt,
		State:            state,
		Session:          view.session,
		Deployment:       newDeploymentStatus(deployed, view.deployedRecord),
		Services:         services,
		RollbackInFlight: newRollbackInFlight(marker),
		Unreadable:       unreadable,
		Notices: newStatusNotices(statusFacts{
			checkedAt:     checkedAt,
			arcaveProject: s.arcaveProject,
			ref:           req.Session,
			state:         state,
			marker:        marker,
			deployed:      deployed,
			view:          view,
			services:      services,
			unreadable:    unreadable,
		}),
	}
	if len(unreadable) > 0 {
		return status, model.ErrStatusPartial
	}
	return status, nil
}

// readContainers reads the compose project's containers, again when one went between the listing
// and its inspect, the only read that can miss: a rollback in flight removes and rebuilds them.
func (s *Service) readContainers(ctx context.Context) (model.ComposeProjectContainers, error) {
	for attempt := 1; ; attempt++ {
		containers, err := s.composeContainers.ReadContainers(ctx)
		switch {
		case err == nil:
			return containers, nil
		case !cerrdefs.IsNotFound(err):
			return model.ComposeProjectContainers{}, err
		case attempt == statusReadAttempts:
			return model.ComposeProjectContainers{}, fmt.Errorf("%w: %w", errComposeProjectChanging, err)
		}
	}
}

// readInflightMarker reads this compose project's inflight marker; another's is not this one's.
func (s *Service) readInflightMarker(ctx context.Context) (*model.InflightMarker, []model.StatusUnreadable) {
	marker, found, err := s.inflight.Get(ctx)
	switch {
	case err != nil:
		return nil, []model.StatusUnreadable{{
			Source: model.StatusUnreadableInflightMarker,
			Error:  err.Error(),
		}}
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
		return storeView{deployedRecord: deployedRecord{
			record:     s.newUnreadableRecord(deployment),
			unreadable: []model.StatusUnreadable{newStoreUnreadable(err)},
		}}, nil
	}
	session, sessionUnreadable, err := s.readResolvedSession(ctx, s.newSessionReader(store), ref)
	if err != nil {
		return storeView{}, err
	}
	deployed := s.readDeployedRecord(ctx, store, deployment)
	return storeView{
		session: session,
		deployedRecord: deployedRecord{
			record:     deployed.record,
			savepoint:  deployed.savepoint,
			unreadable: slices.Concat(sessionUnreadable, deployed.unreadable),
		},
	}, nil
}

// readResolvedSession resolves ref as every command does. A ref naming no session, or several, is
// the user's to fix and the error; any other failure is one thing the status could not read.
func (s *Service) readResolvedSession(
	ctx context.Context, sessions SessionReader, ref string,
) (*model.SessionRef, []model.StatusUnreadable, error) {
	session, err := resolveSession(ctx, sessions, s.current, ref)
	var ambiguous *model.AmbiguousSessionNameError
	switch {
	case err == nil:
		return &model.SessionRef{
			ID:   session.ID,
			Name: session.Name,
		}, nil, nil
	case errors.Is(err, model.ErrSessionNotFound), errors.As(err, &ambiguous):
		return nil, nil, err
	default:
		return nil, []model.StatusUnreadable{{
			Source: model.StatusUnreadableSession,
			Error:  err.Error(),
		}}, nil
	}
}

// resolveSession resolves ref over sessions and the pointer current: the session ref names, else
// the current session, or the default when no pointer is set. A copy of core's.
func resolveSession(
	ctx context.Context, sessions SessionReader, current currentSessionReader, ref string,
) (model.Session, error) {
	if ref != "" {
		listed, err := sessions.ListSessions(ctx)
		if err != nil {
			return model.Session{}, fmt.Errorf("listing sessions: %w", err)
		}
		session, err := model.FindSessionByRef(listed, ref)
		if err != nil {
			return model.Session{}, err
		}
		logResolved(ctx, session, "ref", ref)
		return session, nil
	}
	id, found, err := readCurrentSession(ctx, current)
	if err != nil {
		return model.Session{}, err
	}
	if !found {
		logResolved(ctx, model.DefaultSessionRecord, "default", "")
		return model.DefaultSessionRecord, nil
	}
	listed, err := sessions.ListSessions(ctx)
	if err != nil {
		return model.Session{}, fmt.Errorf("listing sessions: %w", err)
	}
	if session, found := model.FindSessionByID(listed, id.String()); found {
		logResolved(ctx, session, "pointer", "")
		return session, nil
	}
	return model.Session{}, fmt.Errorf("%w: %s, named by %s/current_session; "+
		"run 'arcave session unset' or 'arcave session set <session>'", model.ErrCurrentSessionNotFound, id, model.StateDir)
}

// logResolved records which session a command works in and what named it: a ref, the pointer
// or the default.
func logResolved(ctx context.Context, session model.Session, by, ref string) {
	zerolog.Ctx(ctx).Debug().Str("session", session.ID.String()).Str("name", session.Name).
		Str("by", by).Str("ref", ref).Msg("session resolved")
}

// readCurrentSession reads the current-session pointer, naming the way out of an unreadable one.
func readCurrentSession(ctx context.Context, current currentSessionReader) (model.SessionID, bool, error) {
	id, found, err := current.Get(ctx)
	if err != nil {
		return model.SessionID{}, false, fmt.Errorf("%w; run 'arcave session unset' or 'arcave session set <session>'", err)
	}
	zerolog.Ctx(ctx).Debug().Bool("found", found).Str("session", id.String()).Msg("current-session pointer read")
	return id, found, nil
}

// readDeployedRecord reads the deployed savepoint's record in its own session; a session removed
// since holds none, so it reads missing. Another arcave project's store is not this one's to read.
func (s *Service) readDeployedRecord(
	ctx context.Context, store storages.Navigator, deployment *model.SavepointDeployment,
) deployedRecord {
	if !s.readsRecordOf(deployment) {
		return deployedRecord{}
	}
	savepoints, err := s.loadSavepoint(ctx, store, deployment.Session.ID, deployment.Savepoint.ID)
	switch {
	case err != nil:
		return deployedRecord{
			record:     model.DeploymentRecordUnreadable,
			unreadable: []model.StatusUnreadable{newStoreUnreadable(err)},
		}
	case len(savepoints) == 0:
		return deployedRecord{record: model.DeploymentRecordMissing}
	default:
		return deployedRecord{
			record:    model.DeploymentRecordFound,
			savepoint: &savepoints[0],
		}
	}
}

// loadSavepoint reads the record of savepoint id of the compose project in session; none when it
// has no record.
func (s *Service) loadSavepoint(
	ctx context.Context, store storages.Navigator, session model.SessionID, id model.SavepointID,
) ([]model.Savepoint, error) {
	sessionStore, err := storefactory.Session(store, session)
	if err != nil {
		return nil, fmt.Errorf("open session %s: %w", session, err)
	}
	composeProjectStore, err := storefactory.ComposeProject(sessionStore, s.composeProject)
	if err != nil {
		return nil, fmt.Errorf("open compose project %s: %w", s.composeProject, err)
	}
	savepoints, err := s.newSavepointLoader(composeProjectStore).LoadSavepoints(ctx, []model.SavepointID{id})
	if err != nil {
		return nil, fmt.Errorf("read savepoint %s of compose project %s: %w", id, s.composeProject, err)
	}
	return savepoints, nil
}

// newUnreadableRecord is the record of a deployment the store could not be read for; none for one of
// another arcave project, whose record is not read either way.
func (s *Service) newUnreadableRecord(deployment *model.SavepointDeployment) model.DeploymentRecord {
	if !s.readsRecordOf(deployment) {
		return ""
	}
	return model.DeploymentRecordUnreadable
}

// readsRecordOf reports whether the status reads deployment's record: one of this arcave project.
func (s *Service) readsRecordOf(deployment *model.SavepointDeployment) bool {
	return deployment != nil && deployment.ArcaveProject == s.arcaveProject
}

func newStoreUnreadable(err error) model.StatusUnreadable {
	return model.StatusUnreadable{
		Source: model.StatusUnreadableStore,
		Error:  err.Error(),
	}
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

// newDeploymentStatus is what built the containers, and what the deployed savepoint's record adds;
// nil for a compose project with none.
func newDeploymentStatus(deployed deployedContainers, record deployedRecord) *model.DeploymentStatus {
	switch {
	case deployed.source == "":
		return nil
	case deployed.deployment == nil:
		return &model.DeploymentStatus{Source: deployed.source}
	}
	var (
		deployment  = deployed.deployment
		description string
		restored    *model.DeploymentCounts
	)
	if record.savepoint != nil {
		description = record.savepoint.Description
		restored = &model.DeploymentCounts{
			Volumes:        len(record.savepoint.Stats),
			WritableLayers: len(record.savepoint.WritableLayers),
		}
	}
	return &model.DeploymentStatus{
		Source:        deployed.source,
		DeployedAt:    newOptionalTime(deployment.DeployedAt),
		ArcaveProject: deployment.ArcaveProject,
		Session:       &deployment.Session,
		Savepoint: &model.DeployedSavepointStatus{
			ID:             deployment.Savepoint.ID,
			Name:           deployment.Savepoint.Name,
			CreatedAt:      newOptionalTime(deployment.Savepoint.CreatedAt),
			SafetySnapshot: deployment.Savepoint.IsSafetySnapshot,
			Description:    description,
			Restored:       restored,
		},
		Record: record.record,
	}
}

// newOptionalTime is t, nil when zero.
func newOptionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
