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

// Package inflightmarker keeps the marker of an interrupted rollback on the daemon it guards, as
// the labels of an empty volume per write.
package inflightmarker

import (
	"context"
	"errors"
	"fmt"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// errAnotherComposeProject refuses a marker of a compose project the store does not write.
var errAnotherComposeProject = errors.New("the inflight marker names another compose project")

// volumeDriver is the driver of a marker volume, which holds no data.
const volumeDriver = "local"

// dockerAPI is the slice of the Docker API the store calls.
type dockerAPI interface {
	VolumeList(ctx context.Context, options client.VolumeListOptions) (client.VolumeListResult, error)
	VolumeCreate(ctx context.Context, options client.VolumeCreateOptions) (client.VolumeCreateResult, error)
	VolumeRemove(ctx context.Context, volumeID string, options client.VolumeRemoveOptions) (client.VolumeRemoveResult, error)
}

// Store is the marker of one arcave project on one daemon, written by one compose project; a
// reader takes the arcave project's newest volume.
type Store struct {
	api            dockerAPI
	arcaveProject  model.ArcaveProjectName
	composeProject model.DockerComposeProjectName
}

// NewStore is the marker of arcaveProject that composeProject writes and clears; it never removes
// another compose project's.
func NewStore(api dockerAPI, arcaveProject model.ArcaveProjectName, composeProject model.DockerComposeProjectName) *Store {
	return &Store{
		api:            api,
		arcaveProject:  arcaveProject,
		composeProject: composeProject,
	}
}

// Get reads the newest marker; found is false when none stands. A marker volume whose labels do
// not read is an error naming it.
func (s *Store) Get(ctx context.Context) (marker model.InflightMarker, found bool, err error) {
	volumes, err := s.listVolumes(ctx, s.newArcaveProjectFilter())
	if err != nil {
		return model.InflightMarker{}, false, err
	}
	var (
		newest     model.InflightMarker
		newestName string
	)
	for _, v := range volumes {
		parsed, err := parseInflightMarkerVolume(v.Labels)
		if err != nil {
			return model.InflightMarker{}, false, fmt.Errorf(
				"reading the inflight marker volume %s (docker volume rm %s forgets it): %w", v.Name, v.Name, err)
		}
		if newestName == "" || isNewer(parsed.Marker, v.Name, newest, newestName) {
			newest = parsed.Marker
			newestName = v.Name
		}
	}
	return newest, newestName != "", nil
}

// Put records marker in a new volume, since a volume's labels never change, then removes the
// compose project's older ones; a failed removal is logged and swept by the next write.
func (s *Store) Put(ctx context.Context, marker model.InflightMarker) error {
	if marker.ComposeProject != s.composeProject {
		return fmt.Errorf("%w: the marker names %q, the store %q",
			errAnotherComposeProject, marker.ComposeProject, s.composeProject)
	}
	suffix, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("naming the inflight marker volume: %w", err)
	}
	name := newInflightMarkerVolumeName(s.composeProject, suffix.String())
	markerVolume := inflightMarkerVolume{
		ArcaveProject: s.arcaveProject,
		Marker:        marker,
	}
	if _, err := s.api.VolumeCreate(ctx, client.VolumeCreateOptions{
		Name:   name,
		Driver: volumeDriver,
		Labels: markerVolume.Labels(),
	}); err != nil {
		return fmt.Errorf("creating the inflight marker volume %s: %w", name, err)
	}
	volumes, err := s.listVolumes(ctx, s.newComposeProjectFilter())
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("volume", name).
			Msg("the inflight marker is written; the volumes before it are left for the next write")
		return nil
	}
	for _, v := range volumes {
		if v.Name == name {
			continue
		}
		if err := s.removeVolume(ctx, v.Name); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Str("volume", v.Name).
				Msg("the inflight marker is written; an earlier volume of it is left for the next write")
		}
	}
	return nil
}

// Clear removes the compose project's marker volumes, never another's; none is already cleared.
func (s *Store) Clear(ctx context.Context) error {
	volumes, err := s.listVolumes(ctx, s.newComposeProjectFilter())
	if err != nil {
		return err
	}
	for _, v := range volumes {
		if err := s.removeVolume(ctx, v.Name); err != nil {
			return fmt.Errorf("removing the inflight marker volume %s: %w", v.Name, err)
		}
	}
	return nil
}

// newArcaveProjectFilter selects the arcave project's marker volumes.
func (s *Store) newArcaveProjectFilter() client.Filters {
	return make(client.Filters).Add("label",
		labelArcaveProject.String()+"="+s.arcaveProject.String())
}

// newComposeProjectFilter selects the marker volumes the compose project wrote.
func (s *Store) newComposeProjectFilter() client.Filters {
	return make(client.Filters).Add("label",
		labelArcaveProject.String()+"="+s.arcaveProject.String(),
		labelComposeProject.String()+"="+s.composeProject.String())
}

// listVolumes lists the marker volumes filters select.
func (s *Store) listVolumes(ctx context.Context, filters client.Filters) ([]volume.Volume, error) {
	listed, err := s.api.VolumeList(ctx, client.VolumeListOptions{Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("listing the inflight marker volumes: %w", err)
	}
	return listed.Items, nil
}

// removeVolume removes one marker volume; one already gone is removed.
func (s *Store) removeVolume(ctx context.Context, name string) error {
	if _, err := s.api.VolumeRemove(ctx, name, client.VolumeRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
		return err
	}
	return nil
}

// isNewer reports whether the marker of volume name is newer than other's: by its time, then by
// the volume's name.
func isNewer(marker model.InflightMarker, name string, other model.InflightMarker, otherName string) bool {
	if !marker.At.Equal(other.At) {
		return marker.At.After(other.At)
	}
	return name > otherName
}
