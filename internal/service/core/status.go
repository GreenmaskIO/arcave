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
	"fmt"
	"slices"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/greenmaskio/storages"

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

// StatusDeps are StatusService's collaborators.
type StatusDeps struct {
	ComposeContainers composeContainersReader
	Inflight          inflightMarkerReader
	// Current is the current-session pointer.
	Current currentSessionReader
	// OpenArcaveProjectStore opens the arcave project's store for reading when a status needs it:
	// one that does not open makes the status partial, not a failure.
	OpenArcaveProjectStore func(ctx context.Context) (storages.Storager, error)
	// NewSessions and NewSavepoints read the sessions of an arcave project's store and the
	// savepoints of a compose project's.
	NewSessions   func(store storages.Storager) SessionReader
	NewSavepoints func(store storages.Storager) SavepointReader
	// ArcaveProject is the arcave project the config names; ComposeProject the one -p names.
	ArcaveProject  model.ArcaveProjectName
	ComposeProject model.DockerComposeProjectName
	// Now is the clock CheckedAt is read off.
	Now func() time.Time
}

// StatusService reports what the daemon runs for one compose project. It takes no lock, writes
// nothing, and reads the store only to add to what the daemon's labels say.
type StatusService struct {
	composeContainers      composeContainersReader
	inflight               inflightMarkerReader
	current                currentSessionReader
	openArcaveProjectStore func(ctx context.Context) (storages.Storager, error)
	newSessions            func(store storages.Storager) SessionReader
	newSavepoints          func(store storages.Storager) SavepointReader
	arcaveProject          model.ArcaveProjectName
	composeProject         model.DockerComposeProjectName
	now                    func() time.Time
}

func NewStatusService(deps StatusDeps) *StatusService {
	return &StatusService{
		composeContainers:      deps.ComposeContainers,
		inflight:               deps.Inflight,
		current:                deps.Current,
		openArcaveProjectStore: deps.OpenArcaveProjectStore,
		newSessions:            deps.NewSessions,
		newSavepoints:          deps.NewSavepoints,
		arcaveProject:          deps.ArcaveProject,
		composeProject:         deps.ComposeProject,
		now:                    deps.Now,
	}
}

// storeView is what the store adds to the daemon's answer.
type storeView struct {
	session *model.ResolvedSession
	deployedRecord
}

// deployedRecord is what the store holds of the deployed savepoint, and the savepoints counted
// beside it.
type deployedRecord struct {
	record model.DeploymentRecord
	// listing is the compose project's savepoints in the deployed savepoint's session while the
	// store holds that session, else in the session commands work in; nil when neither was read.
	listing    *savepointListing
	notices    []model.Notice
	unreadable []model.StatusUnreadable
}

// savepointListing is one session's savepoints of the compose project.
type savepointListing struct {
	session    model.SessionID
	savepoints []model.Savepoint
}

// Status reports the compose project as the daemon runs it and what the store adds. An unreadable
// daemon, or a session req names that does not exist, fails it; an unreadable store, current session
// or marker returns it with model.ErrStatusPartial.
func (s *StatusService) Status(ctx context.Context, req model.StatusRequest) (model.ComposeProjectStatus, error) {
	var (
		checkedAt  = s.now().UTC()
		containers model.ComposeProjectContainers
		view       storeView
		err        error
	)
	if containers, err = s.readContainers(ctx); err != nil {
		return model.ComposeProjectStatus{}, err
	}
	var (
		deployed                 = findDeployments(containers)
		services                 = newServiceStatuses(containers, deployed)
		marker, markerUnreadable = s.readInflightMarker(ctx)
	)
	if view, err = s.readStoreView(ctx, req.Session, deployed.primary); err != nil {
		return model.ComposeProjectStatus{}, err
	}
	var (
		unreadable           = slices.Concat([]model.StatusUnreadable{}, markerUnreadable, view.unreadable)
		state                = newComposeProjectState(marker != nil, services)
		latest, count, newer = newSavepointCounts(view.listing, deployed.primary, s.arcaveProject)
	)
	status := model.ComposeProjectStatus{
		ComposeProject:    s.composeProject,
		CheckedAt:         checkedAt,
		State:             state,
		Session:           view.session,
		Deployment:        newDeploymentStatus(deployed, view.record),
		LatestSavepoint:   latest,
		SavepointCount:    count,
		NewerThanDeployed: newer,
		Services:          services,
		RollbackInFlight:  newRollbackInFlight(marker),
		Unreadable:        unreadable,
		Notices: newStatusNotices(statusFacts{
			arcaveProject:  s.arcaveProject,
			composeProject: s.composeProject,
			checkedAt:      checkedAt,
			state:          state,
			marker:         marker,
			deployed:       deployed,
			view:           view,
			services:       services,
			hookRunners:    containers.HookRunners,
			unreadable:     unreadable,
		}),
	}
	if len(unreadable) > 0 {
		return status, model.ErrStatusPartial
	}
	return status, nil
}

// readContainers reads the compose project's containers, again when one went between the listing
// and its inspect, the only read that can miss: a rollback in flight removes and rebuilds them.
func (s *StatusService) readContainers(ctx context.Context) (model.ComposeProjectContainers, error) {
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
func (s *StatusService) readInflightMarker(ctx context.Context) (*model.InflightMarker, []model.StatusUnreadable) {
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

// readStoreView resolves the session the user works in and reads what the store holds of the
// deployed savepoint, or of that session with none deployed. A ref naming no session is its error.
func (s *StatusService) readStoreView(
	ctx context.Context, ref string, deployment *model.SavepointDeployment,
) (storeView, error) {
	store, err := s.openArcaveProjectStore(ctx)
	if err != nil {
		return storeView{deployedRecord: deployedRecord{
			record:     newUnreadableRecord(deployment),
			unreadable: []model.StatusUnreadable{newStoreUnreadable(err)},
		}}, nil
	}
	sessions := s.newSessions(store)
	session, sessionUnreadable, err := s.readResolvedSession(ctx, sessions, ref)
	if err != nil {
		return storeView{}, err
	}
	deployed := s.readDeployedRecord(ctx, store, sessions, session, deployment)
	return storeView{
		session: session,
		deployedRecord: deployedRecord{
			record:     deployed.record,
			listing:    deployed.listing,
			notices:    deployed.notices,
			unreadable: slices.Concat(sessionUnreadable, deployed.unreadable),
		},
	}, nil
}

// readResolvedSession resolves ref as every command does. A ref naming no session, or several, is
// the user's to fix and the error; any other failure is one thing the status could not read.
func (s *StatusService) readResolvedSession(
	ctx context.Context, sessions SessionReader, ref string,
) (*model.ResolvedSession, []model.StatusUnreadable, error) {
	session, resolvedBy, err := resolveSession(ctx, sessions, s.current, ref)
	var ambiguous *model.AmbiguousSessionNameError
	switch {
	case err == nil:
		return &model.ResolvedSession{
			ID:         session.ID,
			Name:       session.Name,
			ResolvedBy: resolvedBy,
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

// readDeployedRecord tells whether the deployed savepoint's session still holds it and lists that
// session, else the session the user works in.
func (s *StatusService) readDeployedRecord(
	ctx context.Context, store storages.Storager, sessions SessionReader,
	current *model.ResolvedSession, deployment *model.SavepointDeployment,
) deployedRecord {
	switch {
	case deployment == nil:
		listing, unreadable := s.readCurrentSavepoints(ctx, store, current)
		return deployedRecord{
			listing:    listing,
			unreadable: unreadable,
		}
	case deployment.ArcaveProject != s.arcaveProject:
		listing, unreadable := s.readCurrentSavepoints(ctx, store, current)
		return deployedRecord{
			listing:    listing,
			notices:    []model.Notice{newOtherArcaveProjectNotice(deployment.ArcaveProject)},
			unreadable: unreadable,
		}
	}

	exists, err := s.sessionExists(ctx, sessions, current, deployment.Session.ID)
	if err != nil {
		return deployedRecord{
			record:     model.DeploymentRecordUnreadable,
			unreadable: []model.StatusUnreadable{newStoreUnreadable(err)},
		}
	}
	if !exists {
		listing, unreadable := s.readCurrentSavepoints(ctx, store, current)
		return deployedRecord{
			record:     model.DeploymentRecordMissing,
			listing:    listing,
			notices:    []model.Notice{newSessionGoneNotice(deployment.Session)},
			unreadable: unreadable,
		}
	}
	listing, err := s.readSavepoints(ctx, store, deployment.Session.ID)
	if err != nil {
		return deployedRecord{
			record:     model.DeploymentRecordUnreadable,
			unreadable: []model.StatusUnreadable{newStoreUnreadable(err)},
		}
	}
	if !slices.ContainsFunc(listing.savepoints, func(savepoint model.Savepoint) bool {
		return savepoint.ID == deployment.Savepoint.ID
	}) {
		return deployedRecord{
			record:  model.DeploymentRecordMissing,
			listing: listing,
			notices: []model.Notice{newSavepointGoneNotice(*deployment)},
		}
	}
	return deployedRecord{
		record:  model.DeploymentRecordFound,
		listing: listing,
	}
}

// readCurrentSavepoints lists the session the user works in; nothing when it was not resolved.
func (s *StatusService) readCurrentSavepoints(
	ctx context.Context, store storages.Navigator, current *model.ResolvedSession,
) (*savepointListing, []model.StatusUnreadable) {
	if current == nil {
		return nil, nil
	}
	listing, err := s.readSavepoints(ctx, store, current.ID)
	if err != nil {
		return nil, []model.StatusUnreadable{newStoreUnreadable(err)}
	}
	return listing, nil
}

// sessionExists tells whether the store holds session id: the default always, the session the
// user works in as resolved, any other by a listing.
func (s *StatusService) sessionExists(
	ctx context.Context, sessions SessionReader, current *model.ResolvedSession, id model.SessionID,
) (bool, error) {
	if id == model.DefaultSession || (current != nil && current.ID == id) {
		return true, nil
	}
	listed, err := sessions.ListSessions(ctx)
	if err != nil {
		return false, fmt.Errorf("listing sessions: %w", err)
	}
	_, found := findSessionByID(listed, id.String())
	return found, nil
}

// readSavepoints lists the compose project's savepoints in session id, safety snapshots included.
func (s *StatusService) readSavepoints(
	ctx context.Context, store storages.Navigator, id model.SessionID,
) (*savepointListing, error) {
	sessionStore, err := storefactory.Session(store, id)
	if err != nil {
		return nil, fmt.Errorf("open session %s: %w", id, err)
	}
	composeProjectStore, err := storefactory.ComposeProject(sessionStore, s.composeProject)
	if err != nil {
		return nil, fmt.Errorf("open compose project %s: %w", s.composeProject, err)
	}
	savepoints, err := s.newSavepoints(composeProjectStore).ListSavepoints(ctx)
	if err != nil {
		return nil, fmt.Errorf("list savepoints of compose project %s: %w", s.composeProject, err)
	}
	return &savepointListing{
		session:    id,
		savepoints: savepoints,
	}, nil
}

// newUnreadableRecord is the record of a deployment the store could not be read for.
func newUnreadableRecord(deployment *model.SavepointDeployment) model.DeploymentRecord {
	if deployment == nil {
		return ""
	}
	return model.DeploymentRecordUnreadable
}

func newStoreUnreadable(err error) model.StatusUnreadable {
	return model.StatusUnreadable{
		Source: model.StatusUnreadableStore,
		Error:  err.Error(),
	}
}

// newSavepointCounts is the listing's newest savepoint, its count and, in the deployed savepoint's
// session of arcaveProject, how many are newer than it; safety snapshots are not counted.
func newSavepointCounts(
	listing *savepointListing, deployment *model.SavepointDeployment, arcaveProject model.ArcaveProjectName,
) (*model.LatestSavepoint, int, *int) {
	if listing == nil {
		return nil, 0, nil
	}
	if deployment != nil && (deployment.ArcaveProject != arcaveProject || deployment.Session.ID != listing.session) {
		deployment = nil
	}
	var (
		latest *model.Savepoint
		count  int
		newer  int
	)
	for _, savepoint := range listing.savepoints {
		if savepoint.IsSafetySnapshot() {
			continue
		}
		count++
		if deployment != nil && savepoint.ID > deployment.Savepoint.ID {
			newer++
		}
		if latest == nil || savepoint.ID > latest.ID {
			latest = &savepoint
		}
	}
	var newerThanDeployed *int
	if deployment != nil {
		newerThanDeployed = &newer
	}
	if latest == nil {
		return nil, count, newerThanDeployed
	}
	return &model.LatestSavepoint{
		Session:   listing.session,
		ID:        latest.ID,
		Name:      latest.Name,
		CreatedAt: latest.CreatedAt,
	}, count, newerThanDeployed
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

// newDeploymentStatus is what built the containers; nil for a compose project with none.
func newDeploymentStatus(deployed deployedContainers, record model.DeploymentRecord) *model.DeploymentStatus {
	switch {
	case deployed.source == "":
		return nil
	case deployed.primary == nil:
		return &model.DeploymentStatus{Source: deployed.source}
	}
	var (
		deployment   = deployed.primary
		revertedFrom *model.SavepointRef
	)
	if deployment.Origin == model.DeploymentOriginRevert {
		revertedFrom = &deployment.RevertedFrom
	}
	return &model.DeploymentStatus{
		Source:        deployed.source,
		ID:            deployment.ID,
		DeployedAt:    newOptionalTime(deployment.At),
		Origin:        deployment.Origin,
		ArcaveProject: deployment.ArcaveProject,
		RevertedFrom:  revertedFrom,
		Session:       &deployment.Session,
		Savepoint: &model.DeployedSavepointStatus{
			ID:                deployment.Savepoint.ID,
			Name:              deployment.Savepoint.Name,
			Description:       deployment.Savepoint.Description,
			CreatedAt:         newOptionalTime(deployment.Savepoint.CreatedAt),
			SafetySnapshot:    deployment.Savepoint.IsSafetySnapshot(),
			SafetySnapshotFor: deployment.Savepoint.SafetySnapshotFor,
			Restored:          deployment.Manifest.Restored,
			NotRestored:       deployment.Manifest.LeftOut,
		},
		Record:        record,
		Host:          deployment.Host,
		ArcaveVersion: deployment.ArcaveVersion,
	}
}

// newOptionalTime is t, nil when zero.
func newOptionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
