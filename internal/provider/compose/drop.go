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

package compose

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// errNetworkStayed is why a network is left that the daemon removed without a word.
var errNetworkStayed = errors.New("still there after its removal")

// resourceReader reports what the daemon holds of the compose project by its label, with none of
// what a full introspection reads.
type resourceReader interface {
	ReadResources(ctx context.Context) (model.ComposeProjectResources, error)
}

// volumesDowner removes everything the daemon holds of a compose project, its volumes too, by the
// project's name alone.
type volumesDowner interface {
	DownWithVolumes(ctx context.Context, composeProject string) error
}

// projectRemover removes by docker name what compose's down leaves of a compose project.
type projectRemover interface {
	RemoveContainer(ctx context.Context, name string) error
	RemoveVolume(ctx context.Context, name string) error
	RemoveNetwork(ctx context.Context, name string) error
}

// layerTags lists the arcave/…:sp-<savepoint> tags the daemon holds and takes one off, never
// forced.
type layerTags interface {
	ListLayerImages(ctx context.Context) ([]model.TaggedImage, error)
	RemoveImageRef(ctx context.Context, ref model.ImageRef) error
}

// surveyedWorkload is the compose project as one label read found it.
type surveyedWorkload struct {
	service   *Provider
	resources model.ComposeProjectResources
}

// Survey reads what the daemon holds of the compose project by its label.
func (s *Provider) Survey(ctx context.Context) (provider.SurveyedWorkload, error) {
	resources, err := s.readResources(ctx)
	if err != nil {
		return nil, err
	}
	return surveyedWorkload{service: s, resources: resources}, nil
}

// Resources names the containers, volumes and networks of the compose project.
func (w surveyedWorkload) Resources() model.DiscardedResources {
	return model.NewDiscardedResources(w.resources)
}

// Present reports whether the daemon holds a container or a volume of the compose project: compose
// would reuse a volume it found, its old data mixed into the build. A network holds no data.
func (w surveyedWorkload) Present() bool {
	return len(w.resources.Containers) > 0 || len(w.resources.Volumes) > 0
}

// Owner judges whose environment the daemon holds by its replicas' deployment labels: it passes
// when every one names session of arcaveProject. A replica with none or unreadable ones is
// NotDeployedByArcave, which wins over one naming elsewhere, DeployedElsewhere; with no replica,
// any container, volume or network left is NotDeployedByArcave, nothing at all passes. Only
// replicas are judged: a one-off or a hook's runner names no deployment. It returns the deployment
// it read, the session's newest build or else the first foreign one.
func (w surveyedWorkload) Owner(
	arcaveProject model.ArcaveProjectName, session model.SessionID,
) (*model.SavepointDeployment, error) {
	var (
		own        *model.SavepointDeployment
		elsewhere  *model.SavepointDeployment
		unlabelled []string
	)
	for _, service := range w.resources.Replicas.Services {
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
	case len(w.resources.Replicas.Services) == 0 && !w.resources.IsEmpty():
		return nil, &model.NotDeployedByArcaveError{
			Containers: w.resources.Containers, Volumes: w.resources.Volumes, Networks: w.resources.Networks,
		}
	default:
		return own, nil
	}
}

// PrepareSavepoint makes the savepoint id names ready to build once the drop has removed what the
// daemon holds: no leftover volume is judged, none surviving the drop.
func (w surveyedWorkload) PrepareSavepoint(
	ctx context.Context, id model.SavepointID, opts provider.PrepareOptions,
) (provider.PreparedSavepoint, error) {
	if err := requireDaemon(ctx, w.service.daemonVersion, w.service.minDaemonMajor); err != nil {
		return nil, err
	}
	savepoint, err := w.service.loadSavepoint(ctx, id)
	if err != nil {
		return nil, err
	}
	return w.service.prepareTarget(ctx, savepoint, opts.Deployment, nil)
}

// Drop removes everything of the compose project and proves it gone; on an empty one it removes
// nothing.
func (w surveyedWorkload) Drop(ctx context.Context) (provider.DroppedWorkload, error) {
	dropped := droppedWorkload{service: w.service}
	if w.resources.IsEmpty() {
		return dropped, nil
	}
	networks, err := w.service.dropComposeProject(ctx, w.resources)
	if err != nil {
		return nil, err
	}
	dropped.networksLeft = networks
	return dropped, nil
}

// droppedWorkload is the compose project dropped, and the networks the drop left.
type droppedWorkload struct {
	service      *Provider
	networksLeft []leftNetwork
}

// Notices names the networks the drop left, and why the daemon kept them.
func (d droppedWorkload) Notices() []model.Notice {
	notices := make([]model.Notice, 0, len(d.networksLeft))
	for _, network := range d.networksLeft {
		notices = append(notices, model.Notice{
			Message: fmt.Sprintf("network %s left behind: %v", network.name, network.cause),
		})
	}
	return notices
}

// Vacant is the dropped compose project ready to build prepared: nothing of it survived, so a
// failed build owes it no volume and orphaned none.
func (d droppedWorkload) Vacant(prepared provider.PreparedSavepoint) (provider.VacantWorkload, error) {
	target, err := toPreparedSavepoint(prepared)
	if err != nil {
		return nil, err
	}
	return vacantWorkload{
		service:              d.service,
		anonymousVolumeNames: map[string]string{},
		ownedBefore:          map[string]bool{},
		downProject:          target.project,
	}, nil
}

// RemoveRestoredLayers takes off every restored layer tag of this arcave project's compose
// project: once it is down no container runs on one, whichever session it was restored in. A tag
// that will not come off is a notice; one already gone is nothing.
func (d droppedWorkload) RemoveRestoredLayers(ctx context.Context) ([]model.ImageRef, []model.Notice) {
	s := d.service
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

// leftNetwork is a network of the compose project its drop left, and why.
type leftNetwork struct {
	name  string
	cause error
}

// dropComposeProject removes everything the daemon holds of the compose project and proves it
// gone. Three passes: the containers other than replicas in before go first, by name with their
// anonymous volumes — compose's down keeps a running one-off of a service with no replica and
// removes a finished one without its volumes; then compose's down by name; then, by docker name,
// what it still left — a container, a volume it passed over (it keys volumes by logical name and
// warns past a busy one), a network one of those held — and a read back. A container or volume
// still there is a ComposeProjectLeftError, whatever outside the compose project holds it left
// alone; a network left holds no data and is returned with the daemon's reason.
func (s *Provider) dropComposeProject(ctx context.Context, before model.ComposeProjectResources) ([]leftNetwork, error) {
	log := zerolog.Ctx(ctx)
	var refusals []error
	removeContainers := func(names []string) {
		for _, name := range names {
			if err := s.projectRemover.RemoveContainer(ctx, name); err != nil {
				log.Warn().Err(err).Str("container", name).Msg("container of the compose project left")
				refusals = append(refusals, err)
			}
		}
	}

	removeContainers(before.NonReplicas())
	if err := s.composeDownWithVolumes(ctx); err != nil {
		return nil, err
	}
	left, err := s.readResources(ctx)
	if err != nil {
		return nil, err
	}
	if left.IsEmpty() {
		return nil, nil
	}

	// The containers first: they hold the volumes and the networks.
	removeContainers(left.Containers)
	for _, name := range left.Volumes {
		if err := s.projectRemover.RemoveVolume(ctx, name); err != nil {
			log.Warn().Err(err).Str("volume", name).Msg("volume of the compose project left")
			refusals = append(refusals, err)
		}
	}
	networkRefusals := map[string]error{}
	for _, name := range left.Networks {
		if err := s.projectRemover.RemoveNetwork(ctx, name); err != nil {
			log.Warn().Err(err).Str("network", name).Msg("network of the compose project left")
			networkRefusals[name] = err
		}
	}
	if left, err = s.readResources(ctx); err != nil {
		return nil, err
	}
	if len(left.Containers) > 0 || len(left.Volumes) > 0 {
		return nil, &model.ComposeProjectLeftError{
			Containers: left.Containers,
			Volumes:    left.Volumes,
			Cause:      errors.Join(refusals...),
		}
	}
	networks := make([]leftNetwork, 0, len(left.Networks))
	for _, name := range left.Networks {
		networks = append(networks, leftNetwork{name: name, cause: cmp.Or(networkRefusals[name], errNetworkStayed)})
	}
	return networks, nil
}

// composeDownWithVolumes runs compose's down of the compose project by its name, its volumes too.
func (s *Provider) composeDownWithVolumes(ctx context.Context) error {
	if err := runComposeActivity(ctx, kindComposeDown, func(ctx context.Context) error {
		return s.volumesDowner.DownWithVolumes(ctx, s.composeProject.String())
	}); err != nil {
		return fmt.Errorf("removing the compose project with its volumes: %w", err)
	}
	return nil
}

// readResources reads what the daemon holds of the compose project by its label.
func (s *Provider) readResources(ctx context.Context) (model.ComposeProjectResources, error) {
	ctx, step := progress.Run(ctx, progress.Step{Kind: kindProjectIntrospect})
	resources, err := s.resourceReader.ReadResources(ctx)
	step.End(err)
	if err != nil {
		return model.ComposeProjectResources{}, fmt.Errorf("read the compose project's resources: %w", err)
	}
	return resources, nil
}
