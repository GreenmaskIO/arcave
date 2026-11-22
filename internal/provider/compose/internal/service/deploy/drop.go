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

package deploy

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// errNetworkStayed is why a network is left that the daemon removed without a word.
var errNetworkStayed = errors.New("still there after its removal")

// LeftNetwork is a network of the compose project its drop left, and why.
type LeftNetwork struct {
	Name  string
	Cause error
}

// Owner judges whose environment the daemon holds by its replicas' deployment labels: it passes
// when every one names session of arcaveProject. A replica with none or unreadable ones is
// NotDeployedByArcave, which wins over one naming elsewhere, DeployedElsewhere; with no replica,
// any container, volume or network left is NotDeployedByArcave, nothing at all passes. Only
// replicas are judged: a one-off or a hook's runner names no deployment. It returns the deployment
// it read, the session's newest build or else the first foreign one.
func Owner(
	resources composemodel.ComposeProjectResources, arcaveProject model.ArcaveProjectName, session model.SessionID,
) (*model.SavepointDeployment, error) {
	var (
		own        *model.SavepointDeployment
		elsewhere  *model.SavepointDeployment
		unlabelled []string
	)
	for _, service := range resources.Replicas {
		for _, replica := range service.Containers {
			deployment, ok, err := composemodel.ParseDeploymentLabels(replica.Labels)
			switch {
			case err != nil || !ok:
				unlabelled = append(unlabelled, fmt.Sprintf("%s-%d", service.Name, replica.Number))
			case deployment.ArcaveProject != arcaveProject || deployment.Session.ID != session:
				if elsewhere == nil {
					elsewhere = &deployment.SavepointDeployment
				}
			case own == nil || deployment.DeployedAt.After(own.DeployedAt):
				own = &deployment.SavepointDeployment
			}
		}
	}
	read := own
	if read == nil {
		read = elsewhere
	}
	switch {
	case len(unlabelled) > 0:
		return read, &composemodel.NotDeployedByArcaveError{Replicas: unlabelled}
	case elsewhere != nil:
		return read, &model.DeployedElsewhereError{ArcaveProject: arcaveProject, Deployment: *elsewhere}
	case len(resources.Replicas) == 0 && !resources.IsEmpty():
		return nil, &composemodel.NotDeployedByArcaveError{
			Containers: resources.Containers,
			Volumes:    resources.Volumes,
			Networks:   resources.Networks,
		}
	default:
		return own, nil
	}
}

// Drop removes everything the daemon holds of the compose project and proves it gone. Three
// passes: the containers other than replicas in before go first, by name with their anonymous
// volumes — compose's down keeps a running one-off of a service with no replica and removes a
// finished one without its volumes; then compose's down by name; then, by docker name, what it
// still left — a container, a volume it passed over (it keys volumes by logical name and warns past
// a busy one), a network one of those held — and a read back. A container or volume still there is
// a ComposeProjectLeftError, whatever outside the compose project holds it left alone; a network
// left holds no data and is returned with the daemon's reason.
func (s *Deployer) Drop(ctx context.Context, before composemodel.ComposeProjectResources) ([]LeftNetwork, error) {
	log := zerolog.Ctx(ctx)
	var refusals []error
	removeContainers := func(names []string) {
		for _, name := range names {
			if err := s.projectRemover.RemoveContainer(ctx, name); err != nil {
				log.Debug().Err(err).Str("container", name).Msg("container of the compose project left")
				refusals = append(refusals, err)
			}
		}
	}

	removeContainers(before.NonReplicas())
	if err := s.composeDownWithVolumes(ctx); err != nil {
		return nil, err
	}
	left, err := s.inspector.ReadResources(ctx)
	if err != nil {
		return nil, err
	}
	if left.IsEmpty() {
		return nil, nil
	}

	// The containers first: they hold the volumes and the networks.
	removeContainers(left.Containers)
	for _, name := range left.Volumes {
		if err := s.volumeRemover.RemoveVolume(ctx, name); err != nil {
			log.Debug().Err(err).Str("volume", name).Msg("volume of the compose project left")
			refusals = append(refusals, err)
		}
	}
	networkRefusals := map[string]error{}
	for _, name := range left.Networks {
		if err := s.projectRemover.RemoveNetwork(ctx, name); err != nil {
			log.Debug().Err(err).Str("network", name).Msg("network of the compose project left")
			networkRefusals[name] = err
		}
	}
	if left, err = s.inspector.ReadResources(ctx); err != nil {
		return nil, err
	}
	if len(left.Containers) > 0 || len(left.Volumes) > 0 {
		return nil, &composemodel.ComposeProjectLeftError{
			Containers: left.Containers,
			Volumes:    left.Volumes,
			Cause:      errors.Join(refusals...),
		}
	}
	networks := make([]LeftNetwork, 0, len(left.Networks))
	for _, name := range left.Networks {
		networks = append(networks, LeftNetwork{
			Name:  name,
			Cause: cmp.Or(networkRefusals[name], errNetworkStayed),
		})
	}
	return networks, nil
}

// RemoveRestoredLayers takes off every restored layer tag of this arcave project's compose
// project: once it is down no container runs on one, whichever session it was restored in. A tag
// that will not come off is a notice; one already gone is nothing.
func (s *Deployer) RemoveRestoredLayers(ctx context.Context) ([]model.ImageRef, []model.Notice) {
	tagged, err := s.layerTags.ListLayerImages(ctx)
	if err != nil {
		notice := fmt.Sprintf("the restored layer tags are left behind: %s", err)
		progress.Current(ctx).WarnInResult(notice)
		return []model.ImageRef{}, []model.Notice{{Message: notice}}
	}
	var refs []model.ImageRef
	for _, image := range tagged {
		if image.Origin == s.layerOrigin {
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
		case errors.Is(err, composemodel.ErrImageRefNotFound):
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

// composeDownWithVolumes runs compose's down of the compose project by its name, its volumes too.
func (s *Deployer) composeDownWithVolumes(ctx context.Context) error {
	if err := runComposeActivity(ctx, kindComposeDown, func(ctx context.Context) error {
		return s.composeRunner.DownWithVolumes(ctx, s.layerOrigin.ComposeProject.String())
	}); err != nil {
		return fmt.Errorf("removing the compose project with its volumes: %w", err)
	}
	return nil
}
