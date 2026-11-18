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
	"fmt"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"maps"
	"slices"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/pkg/progress"

	"github.com/greenmaskio/arcave/internal/model"
)

// replicaPair is one recorded replica and the replica compose created in its place.
type replicaPair struct {
	recorded int
	created  int
}

// recordedReplica is one replica a savepoint recorded and the image a rollback builds
// it on: its restored layer's tag, or its service's own image.
type recordedReplica struct {
	service       string
	number        int
	containerName string
	image         string
}

// replicaRebuild is one recorded replica and the created container it is rebuilt over.
type replicaRebuild struct {
	recorded    recordedReplica
	created     int
	containerID string
}

// rebuildDivergentReplicas rebuilds each replica the compose spec cannot name on its own layer
// and recorded number. It must run before the volume restore, which resolves through containers.
func (s *Deployer) rebuildDivergentReplicas(ctx context.Context, replicas []recordedReplica) error {
	if createdAsRecorded(replicas) {
		return nil
	}

	introspection, err := s.composeReader.Introspect(ctx)
	if err != nil {
		return fmt.Errorf("introspecting the rebuilt compose project: %w", err)
	}
	rebuilds, err := newReplicaRebuilds(replicas, builtContainers(introspection))
	if err != nil {
		return err
	}

	ctx, batch := progress.Batch(ctx, progress.Step{Kind: kindReplicaRebuild}, len(rebuilds))
	for _, rebuild := range rebuilds {
		if err := s.rebuildReplica(ctx, rebuild); err != nil {
			batch.End(err)
			return err
		}
	}
	batch.End(nil)
	return nil
}

// rebuildReplica recreates one created container as the replica the savepoint recorded.
func (s *Deployer) rebuildReplica(ctx context.Context, rebuild replicaRebuild) error {
	log := zerolog.Ctx(ctx)
	replica := rebuild.recorded
	ctx, step := progress.Run(ctx, progress.Step{Kind: kindReplica, ID: replica.containerName})
	if replica.number != rebuild.created {
		log.Info().Str("service", replica.service).Int("replica", replica.number).Int("created_replica", rebuild.created).
			Msg("compose numbers replicas without gaps and created this one under another number; " +
				"it is recreated under the number and name the savepoint recorded")
		step.Warn(fmt.Sprintf("created as replica %d, recreated as %d", rebuild.created, replica.number))
	}
	identity := composemodel.ReplicaIdentity{Name: replica.containerName, Number: replica.number}
	rebuilt, err := s.containerRebuilder.RecreateWithImage(ctx, rebuild.containerID, identity, replica.image)
	step.End(err)
	if err != nil {
		return fmt.Errorf("rebuild replica %d of service %s from %s: %w",
			replica.number, replica.service, replica.image, err)
	}
	log.Info().Str("service", replica.service).Int("replica", replica.number).
		Str("container", rebuilt).Str("image", replica.image).Msg("replica rebuilt as the savepoint recorded it")
	return nil
}

// newRecordedReplicas lists every replica savepoint holds under the number and name it
// recorded: one with a restored layer on its tag, one without on its service's own
// image. A restore resolves an anonymous volume by recorded number, so a gap in the
// numbering is rebuilt without a layer too.
func newRecordedReplicas(
	savepoint model.Savepoint, project *types.Project, restored []model.WritableLayerRestoreResult,
) []recordedReplica {
	replicas := make([]recordedReplica, 0, len(restored))
	for _, layer := range restored {
		replicas = append(replicas, recordedReplica{
			service:       layer.Service,
			number:        layer.Replica,
			containerName: layer.ContainerName,
			image:         layer.Tag,
		})
	}
	if savepoint.NotCovered == nil {
		return replicas
	}
	for _, skipped := range savepoint.NotCovered.WritableLayers {
		svc, ok := project.Services[skipped.Service]
		if !ok {
			continue
		}
		replicas = append(replicas, recordedReplica{
			service:       skipped.Service,
			number:        skipped.Replica,
			containerName: skipped.Container,
			image:         svc.Image,
		})
	}
	return replicas
}

// replicasByService groups the replicas by service, each group ascending by replica,
// and lists the services in the order they first appear.
func replicasByService(replicas []recordedReplica) ([]string, map[string][]recordedReplica) {
	var services []string
	byService := map[string][]recordedReplica{}
	for _, replica := range replicas {
		if _, ok := byService[replica.service]; !ok {
			services = append(services, replica.service)
		}
		byService[replica.service] = append(byService[replica.service], replica)
	}
	for _, group := range byService {
		slices.SortFunc(group, func(a, b recordedReplica) int { return cmp.Compare(a.number, b.number) })
	}
	return services, byService
}

// createdAsRecorded reports whether every service's replicas count from 1 without a gap,
// all on the one image the service was retargeted to. It trusts compose to number a
// freshly created service 1..N, which is what lets it skip reading the created containers.
func createdAsRecorded(replicas []recordedReplica) bool {
	_, byService := replicasByService(replicas)
	for _, group := range byService {
		for i, replica := range group {
			if replica.number != i+1 || replica.image != group[0].image {
				return false
			}
		}
	}
	return true
}

// newReplicaRebuilds is the replicas whose image or number compose did not reproduce, highest
// recorded number first per service so each claimed name has already been freed.
func newReplicaRebuilds(replicas []recordedReplica, built map[string]map[int]string) ([]replicaRebuild, error) {
	services, byService := replicasByService(replicas)
	var rebuilds []replicaRebuild
	for _, service := range services {
		group := byService[service]
		created := slices.Sorted(maps.Keys(built[service]))
		pairs, err := newReplicaPairs(replicaNumbers(group), created)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", service, err)
		}
		for i := len(group) - 1; i >= 0; i-- {
			replica, pair := group[i], pairs[i]
			if replica.image == group[0].image && pair.recorded == pair.created {
				continue
			}
			rebuilds = append(rebuilds, replicaRebuild{
				recorded: replica, created: pair.created, containerID: built[service][pair.created],
			})
		}
	}
	return rebuilds, nil
}

// newReplicaPairs pairs recorded with created replica numbers, both ascending: [1,3] against
// [1,2] is 1←1, 3←2. Fewer created than recorded is an error.
func newReplicaPairs(recorded, created []int) ([]replicaPair, error) {
	recorded, created = slices.Sorted(slices.Values(recorded)), slices.Sorted(slices.Values(created))
	if len(created) < len(recorded) {
		return nil, fmt.Errorf("the rebuilt compose project holds %d replicas to restore %d recorded ones into",
			len(created), len(recorded))
	}
	pairs := make([]replicaPair, len(recorded))
	for i, number := range recorded {
		pairs[i] = replicaPair{recorded: number, created: created[i]}
	}
	return pairs, nil
}

// replicaNumbers is the number of each replica, in order.
func replicaNumbers(replicas []recordedReplica) []int {
	numbers := make([]int, len(replicas))
	for i, replica := range replicas {
		numbers[i] = replica.number
	}
	return numbers
}

// builtContainers indexes the compose project's containers by service and replica number.
func builtContainers(introspection composemodel.IntrospectionResult) map[string]map[int]string {
	built := map[string]map[int]string{}
	for _, svc := range introspection.Project.Services {
		for _, container := range svc.Containers {
			if built[svc.Name] == nil {
				built[svc.Name] = map[int]string{}
			}
			built[svc.Name][container.Number] = container.ID
		}
	}
	return built
}
