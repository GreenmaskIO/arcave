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
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// clearedSafetySnapshotNotice says what a forced down leaves of the rollback whose marker it
// cleared: the safety snapshot is the last copy of the state before it, and nothing guards it now.
const clearedSafetySnapshotNotice = "the inflight marker of an interrupted rollback was cleared; " +
	"its safety snapshot %s is the last copy of the state before that rollback, and a gc may now retire it"

// layerTags lists the arcave/…:sp-<savepoint> tags the daemon holds and takes one off, never
// forced: a down drops the restored layers of the containers it removed.
type layerTags interface {
	ListLayerImages(ctx context.Context) ([]model.TaggedImage, error)
	RemoveImageRef(ctx context.Context, ref model.ImageRef) error
}

// DownDeps are DownService's collaborators: a down reads the daemon and the inflight marker, and
// writes nothing to the store, so it takes neither a session lock nor a write admission.
type DownDeps struct {
	ProjectLocker projectLocker
	Inflight      inflightMarkers
	// Sessions names Session in the result.
	Sessions sessionLookup
	// Session is the session the down runs in; zero means model.DefaultSession.
	Session        model.SessionID
	ArcaveProject  model.ArcaveProjectName
	ComposeProject model.DockerComposeProjectName
	Resources      composeResourcesReader
	ComposeRunner  composeVolumesDowner
	// Remover removes, by docker name, a container or a volume compose's down left.
	Remover   projectRemover
	LayerTags layerTags
}

// DownService ends a compose project's environment on the daemon.
type DownService struct {
	projectLocker  projectLocker
	inflightMarker inflightMarkers
	sessions       sessionLookup
	session        model.SessionID
	arcaveProject  model.ArcaveProjectName
	composeProject model.DockerComposeProjectName
	resources      composeResourcesReader
	composeRunner  composeVolumesDowner
	remover        projectRemover
	layerTags      layerTags
}

func NewDownService(deps DownDeps) *DownService {
	return &DownService{
		projectLocker:  deps.ProjectLocker,
		inflightMarker: deps.Inflight,
		sessions:       deps.Sessions,
		session:        cmp.Or(deps.Session, model.DefaultSession),
		arcaveProject:  deps.ArcaveProject,
		composeProject: deps.ComposeProject,
		resources:      deps.Resources,
		composeRunner:  deps.ComposeRunner,
		remover:        deps.Remover,
		layerTags:      deps.LayerTags,
	}
}

// Down takes off the daemon everything of the compose project — containers, networks, the volumes
// it owns, its containers' anonymous volumes and its restored layer tags — once every replica's
// deployment labels name this session of this arcave project. A bind and an external volume stay.
// What changed since the last savepoint is lost: a down takes none. req.Force skips the check.
func (s *DownService) Down(ctx context.Context, req model.DownRequest) (_ model.DownResult, err error) {
	ctx, step := progress.Begin(ctx, progress.Step{Kind: kindDown})
	defer func() { step.End(err) }()

	// The flock alone: the down writes nothing to the store, and the daemon is host-local, as the
	// lock and the marker are.
	return withUnlock(ctx, s.projectLocker, func() (model.DownResult, error) {
		return s.down(ctx, req)
	})
}

// down is the workflow itself, run with the project lock held.
func (s *DownService) down(ctx context.Context, req model.DownRequest) (model.DownResult, error) {
	marker, err := s.requireDownMarker(ctx, req.Force)
	if err != nil {
		return model.DownResult{}, err
	}
	session, err := readSessionRef(ctx, s.sessions, s.session)
	if err != nil {
		return model.DownResult{}, err
	}
	resources, err := readResources(ctx, s.resources)
	if err != nil {
		return model.DownResult{}, preferInterrupt(ctx, err)
	}
	deployment, err := findDeploymentOwner(resources, s.arcaveProject, session.ID)
	if err != nil && !req.Force {
		return model.DownResult{}, err
	}

	// Proved gone, not taken on compose's word: what is left keeps the marker and the tags.
	var networksLeft []leftNetwork
	if !resources.IsEmpty() {
		if networksLeft, err = dropComposeProject(ctx, s.composeRunner, s.resources, s.remover, s.composeProject, resources); err != nil {
			return model.DownResult{}, preferInterrupt(ctx, err)
		}
	}

	// On a revertContext: an interrupt past the drop must not leave the marker of an environment
	// that is gone.
	cleanupCtx, cancel := revertContext(ctx)
	defer cancel()
	markerNotices, err := s.clearMarker(cleanupCtx, marker)
	if err != nil {
		return model.DownResult{}, err
	}
	removedTags, tagNotices := s.removeRestoredTags(cleanupCtx)
	return model.DownResult{
		ComposeProject:  s.composeProject,
		Session:         session,
		Deployment:      deployment,
		Forced:          req.Force,
		ClearedInflight: marker,
		Discarded:       model.NewDiscardedResources(resources),
		RemovedTags:     removedTags,
		Notices:         slices.Concat(newNetworkNotices(cleanupCtx, networksLeft), markerNotices, tagNotices),
	}, nil
}

// newNetworkNotices says which networks of the compose project its drop left, and why, as the
// daemon refused them.
func newNetworkNotices(ctx context.Context, networks []leftNetwork) []model.Notice {
	notices := make([]model.Notice, 0, len(networks))
	for _, network := range networks {
		notice := fmt.Sprintf("network %s left behind: %v", network.name, network.cause)
		progress.Current(ctx).WarnInResult(notice)
		notices = append(notices, model.Notice{Message: notice})
	}
	return notices
}

// requireDownMarker refuses a down while an inflight marker stands: another compose project's
// always, as it guards that one's environment, and this one's — from any session — unless force,
// which returns it for the down to clear once the environment it guards is gone.
func (s *DownService) requireDownMarker(ctx context.Context, force bool) (*model.InflightMarker, error) {
	marker, found, err := s.inflightMarker.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the inflight marker: %w", err)
	}
	if !found {
		return nil, nil
	}
	if force && marker.ComposeProject == s.composeProject {
		return &marker, nil
	}
	err = &model.InterruptedRollbackError{Marker: marker}
	zerolog.Ctx(ctx).Error().Err(err).
		Str("session", marker.Session.String()).
		Str("compose_project", marker.ComposeProject.String()).
		Msg(recoveryHint(marker))
	return nil, err
}

// clearMarker clears the marker a forced down took over, nil for none, and says what is left of
// the rollback it recorded.
func (s *DownService) clearMarker(ctx context.Context, marker *model.InflightMarker) ([]model.Notice, error) {
	if marker == nil {
		return nil, nil
	}
	if err := s.inflightMarker.Clear(ctx); err != nil {
		return nil, fmt.Errorf("the environment is down; clearing the inflight marker: %w", err)
	}
	if marker.Safety == "" {
		return nil, nil
	}
	notice := fmt.Sprintf(clearedSafetySnapshotNotice, marker.Safety)
	progress.Current(ctx).WarnInResult(notice)
	return []model.Notice{{Message: notice, Severe: true}}, nil
}

// removeRestoredTags takes off every restored layer tag of this arcave project's compose project:
// once it is down no container runs on one, whichever session it was restored in. A tag that
// will not come off is a notice, never the down's failure; one already gone is nothing.
func (s *DownService) removeRestoredTags(ctx context.Context) ([]model.ImageRef, []model.Notice) {
	origin := model.LayerOrigin{ArcaveProject: s.arcaveProject, ComposeProject: s.composeProject}
	tagged, err := s.layerTags.ListLayerImages(ctx)
	if err != nil {
		notice := fmt.Sprintf("the restored layer tags are left behind: %s", err)
		progress.Current(ctx).WarnInResult(notice)
		return []model.ImageRef{}, []model.Notice{{Message: notice}}
	}
	var refs []model.ImageRef
	for _, image := range tagged {
		if image.Origin == origin {
			refs = append(refs, image.Ref)
		}
	}

	ctx, batch := progress.Batch(ctx, progress.Step{Kind: kindDownUntag}, len(refs))
	defer batch.End(nil)
	var (
		removed = []model.ImageRef{}
		notices []model.Notice
	)
	for _, ref := range refs {
		tagCtx, step := progress.Run(ctx, progress.Step{Kind: kindTag, Name: string(ref)})
		err := s.layerTags.RemoveImageRef(tagCtx, ref)
		switch {
		case errors.Is(err, model.ErrImageRefNotFound):
			step.End(nil)
		case err != nil:
			notice := fmt.Sprintf("restored layer tag %s left behind: %s", ref, err)
			step.WarnInResult(notice)
			step.End(nil)
			notices = append(notices, model.Notice{Message: notice})
		default:
			step.End(nil)
			removed = append(removed, ref)
		}
	}
	return removed, notices
}

// findDeploymentOwner judges whose environment the daemon holds by its replicas' deployment
// labels: it passes when every one names this session of arcaveProject. A replica with none or
// unreadable ones is NotDeployedByArcave, which wins over one naming elsewhere, DeployedElsewhere;
// with no replica, any container, volume or network left is NotDeployedByArcave, nothing at all
// passes. Only replicas are judged: a one-off or a hook's runner names no deployment. It
// returns the deployment it read, the session's newest build or else the first foreign one.
func findDeploymentOwner(
	resources model.ComposeProjectResources, arcaveProject model.ArcaveProjectName, session model.SessionID,
) (*model.SavepointDeployment, error) {
	var (
		own        *model.SavepointDeployment
		elsewhere  *model.SavepointDeployment
		unlabelled []string
	)
	for _, service := range resources.Replicas.Services {
		for _, replica := range service.Containers {
			deployment, ok, err := model.ParseSavepointDeployment(replica.Labels)
			switch {
			case err != nil || !ok:
				unlabelled = append(unlabelled, fmt.Sprintf("%s-%d", service.Name, replica.Number))
			case deployment.ArcaveProject != arcaveProject || deployment.Session.ID != session:
				if elsewhere == nil {
					elsewhere = &deployment
				}
			case own == nil || deployment.DeployedAt.After(own.DeployedAt):
				own = &deployment
			}
		}
	}
	read := own
	if read == nil {
		read = elsewhere
	}
	switch {
	case len(unlabelled) > 0:
		return read, &model.NotDeployedByArcaveError{Replicas: unlabelled}
	case elsewhere != nil:
		return read, &model.DeployedElsewhereError{ArcaveProject: arcaveProject, Deployment: *elsewhere}
	case len(resources.Replicas.Services) == 0 && !resources.IsEmpty():
		return nil, &model.NotDeployedByArcaveError{
			Containers: resources.Containers, Volumes: resources.Volumes, Networks: resources.Networks,
		}
	default:
		return own, nil
	}
}
