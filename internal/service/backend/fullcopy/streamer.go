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

// Package fullcopy captures a volume wholesale: one tar archive per volume per
// snapshot, under <snapshotID>/<objectID>/, streamed through the Docker engine
// and stored verbatim so ownership and permissions survive a restore.
//
// The volume is mounted directly and streamed through a container of arcave's
// own, so a service container is never involved and a mount's subpath never
// narrows what the archive holds.
//
// Wholesale is what the name carries: the whole volume in one archive, as
// against cdc, which writes only the chunks that changed.
package fullcopy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// archiveName is the one object a capture consists of.
const archiveName = "data.tar"

// errNoStore refuses a spec carrying no root. This backend keeps its captures in
// the tree it was handed, so without one there is nowhere to write.
var errNoStore = errors.New("backend spec carries no store")

// VolumeStreamer is what the backend needs from the Docker engine: the mount
// source of a volume, a stream of the whole volume out, and the same stream wiped and
// extracted back in. The concrete *docker.Engine satisfies it.
type VolumeStreamer interface {
	ResolveMountSource(ctx context.Context, v model.Volume) (model.MountSource, error)
	OpenVolume(ctx context.Context, src model.MountSource) (io.ReadCloser, error)
	WriteVolume(ctx context.Context, src model.MountSource, r io.Reader) error
}

// Factory builds fullcopy snapshotters over one shared Docker engine.
type Factory struct {
	docker VolumeStreamer
}

// NewFactory builds the factory. Not New, so that building off it does not read
// New(docker).New(spec) at every call site.
func NewFactory(docker VolumeStreamer) *Factory { return &Factory{docker: docker} }

func (*Factory) Kind() model.SnapshotBackendKind { return model.SnapshotBackendFullCopy }

// New binds the instance to the root the caller handed it — the one thing fixed
// for its life. The volume's mount source is not, and cannot be: resolving it may
// reach the daemon, and a constructor has no ctx to do that on.
func (s *Factory) New(spec model.BackendSpec) (model.VolumeSnapshotBackend, error) {
	if spec.Store == nil {
		return nil, errNoStore
	}
	return &Snapshotter{volume: spec.Volume, store: spec.Store, docker: s.docker}, nil
}

// Snapshotter captures one volume into one root.
type Snapshotter struct {
	volume model.Volume
	store  storages.Navigator
	docker VolumeStreamer
}

func (s *Snapshotter) Volume() model.Volume { return s.volume }

// Snapshot streams the whole volume out and stores it verbatim.
func (s *Snapshotter) Snapshot(
	ctx context.Context, req model.BackendSnapshotRequest,
) (model.SnapshotStat, error) {
	captureStore, err := s.captureStore(req.SnapshotID)
	if err != nil {
		return model.SnapshotStat{}, err
	}

	src, err := s.mountSource(ctx)
	if err != nil {
		return model.SnapshotStat{}, err
	}

	archive, err := s.docker.OpenVolume(ctx, src)
	if err != nil {
		return model.SnapshotStat{}, fmt.Errorf("open docker volume %s: %w", src.Source, err)
	}
	defer func() {
		if err := archive.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close docker mount")
		}
	}()

	// The counter sits in the stream, so the size reported is the size
	// actually written.
	counted := utils.NewCountingReader(archive)
	if err := captureStore.PutObject(ctx, archiveName, progress.NewReader(ctx, counted, 0)); err != nil {
		return model.SnapshotStat{}, fmt.Errorf("write archive: %w", err)
	}
	return model.SnapshotStat{
		Volume:      s.volume,
		Type:        model.SnapshotBackendFullCopy,
		SizeBytes:   counted.N(),
		StoredBytes: counted.N(),
	}, nil
}

// Restore hands the stored tar stream to WriteVolume, which wipes the volume
// and extracts into it. req.Payload is not read: the key is recomputed, and
// req.SizeBytes is the stream's own length, the capture's counted size.
func (s *Snapshotter) Restore(ctx context.Context, req model.BackendRestoreRequest) error {
	captureStore, err := s.captureStore(req.SnapshotID)
	if err != nil {
		return err
	}

	src, err := s.mountSource(ctx)
	if err != nil {
		return err
	}

	archive, err := captureStore.GetObject(ctx, archiveName)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() {
		if err := archive.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close archive")
		}
	}()

	if err := s.docker.WriteVolume(ctx, src, progress.NewReader(ctx, archive, req.SizeBytes)); err != nil {
		return fmt.Errorf("write docker volume %s: %w", src.Source, err)
	}
	return nil
}

// mountSource is what a container mounts to reach this volume. Resolved per call
// rather than held on the instance: it may cost a round trip to the daemon, and
// only the two methods that move bytes need it.
func (s *Snapshotter) mountSource(ctx context.Context) (model.MountSource, error) {
	src, err := s.docker.ResolveMountSource(ctx, s.volume)
	if err != nil {
		return model.MountSource{}, fmt.Errorf("resolve the mount source of volume %s: %w", s.volume.ObjectID, err)
	}
	return src, nil
}

// Remove deletes the archive and stops: a Storager may not delete its own root,
// so the caller sweeps the directory. An archive already gone is not an error.
func (s *Snapshotter) Remove(ctx context.Context, req model.BackendRemoveRequest) error {
	captureStore, err := s.captureStore(req.SnapshotID)
	if err != nil {
		return err
	}
	if err := captureStore.Delete(ctx, archiveName); err != nil && !errors.Is(err, storages.ErrFileNotFound) {
		return fmt.Errorf("delete archive: %w", err)
	}
	return nil
}

// captureStore narrows the root to the one capture a request addresses.
// It is the only place an id becomes a storage key, so the id gate stands here:
// the destructive paths render a key through it too.
func (s *Snapshotter) captureStore(snapshotID model.SavepointID) (storages.Storager, error) {
	if err := snapshotID.Validate(); err != nil {
		return nil, err
	}
	captureStore, err := s.store.SubStorage(path.Join(snapshotID.String(), s.volume.ObjectID), true)
	if err != nil {
		return nil, fmt.Errorf("re-root store at capture %s: %w", snapshotID, err)
	}
	return captureStore, nil
}
