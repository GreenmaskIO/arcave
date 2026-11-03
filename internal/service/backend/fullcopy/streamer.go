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
// and stored zstd-compressed (data.tar.zst) or as is (data.tar), so ownership
// and permissions survive a restore. The record's payload names the format.
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

// Options is what every capture of one factory shares.

// Factory builds fullcopy snapshotters over one shared Docker engine.
type Factory struct {
	docker      VolumeStreamer
	compression Compression
}

// NewFactory builds the factory. Not New, so that building off it does not read
// New(docker).New(spec) at every call site.
func NewFactory(docker VolumeStreamer, compression Compression) *Factory {
	return &Factory{
		docker:      docker,
		compression: compression,
	}
}

func (*Factory) Kind() model.SnapshotBackendKind { return model.SnapshotBackendFullCopy }

// New binds the instance to the root the caller handed it — the one thing fixed
// for its life. The volume's mount source is not, and cannot be: resolving it may
// reach the daemon, and a constructor has no ctx to do that on.
func (s *Factory) New(spec model.BackendSpec) (model.VolumeSnapshotBackend, error) {
	if spec.Store == nil {
		return nil, errNoStore
	}
	if err := s.compression.Validate(); err != nil {
		return nil, err
	}
	return &Snapshotter{
		volume:      spec.Volume,
		store:       spec.Store,
		docker:      s.docker,
		compression: s.compression,
	}, nil
}

// Snapshotter captures one volume into one root.
type Snapshotter struct {
	volume      model.Volume
	store       storages.Navigator
	docker      VolumeStreamer
	compression Compression
}

func (s *Snapshotter) Volume() model.Volume { return s.volume }

// Snapshot streams the whole volume out and stores it in the factory's format.
// SizeBytes counts the tar stream, StoredBytes what reached the store.
func (s *Snapshotter) Snapshot(
	ctx context.Context, req model.BackendSnapshotRequest,
) (model.SnapshotStat, error) {
	var (
		captureStore storages.Storager
		src          model.MountSource
		archive      io.ReadCloser
		err          error
	)
	if captureStore, err = s.captureStore(req.SnapshotID); err != nil {
		return model.SnapshotStat{}, err
	}
	if src, err = s.mountSource(ctx); err != nil {
		return model.SnapshotStat{}, err
	}

	if archive, err = s.docker.OpenVolume(ctx, src); err != nil {
		return model.SnapshotStat{}, fmt.Errorf("open docker volume %s: %w", src.Source, err)
	}
	defer func() {
		if err := archive.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close docker mount")
		}
	}()

	// Both counters sit in the stream: the tar's ahead of the encoder, the
	// store's behind it.
	size := utils.NewCountingReader(archive)
	encoded := newEncoder(ctx, s.compression, progress.NewReader(ctx, size, 0))
	defer func() {
		if err := encoded.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close encoder")
		}
	}()
	stored := utils.NewCountingReader(encoded)
	if err := captureStore.PutObject(ctx, s.compression.archiveName(), stored); err != nil {
		return model.SnapshotStat{}, fmt.Errorf("write archive: %w", err)
	}
	return model.SnapshotStat{
		Volume:      s.volume,
		Type:        model.SnapshotBackendFullCopy,
		SizeBytes:   size.N(),
		StoredBytes: stored.N(),
		Payload:     Payload{Compression: s.compression},
	}, nil
}

// Restore decodes the stored archive and hands the tar stream to WriteVolume,
// which wipes the volume and extracts into it. The record's payload names the
// format; req.SizeBytes is the tar stream's length, the capture's counted size.
func (s *Snapshotter) Restore(ctx context.Context, req model.BackendRestoreRequest) error {
	var (
		captureStore storages.Storager
		payload      Payload
		src          model.MountSource
		archive      io.ReadCloser
		tarStream    io.ReadCloser
		err          error
	)
	if captureStore, err = s.captureStore(req.SnapshotID); err != nil {
		return err
	}
	if payload, err = DecodePayload(req.Payload); err != nil {
		return err
	}
	if src, err = s.mountSource(ctx); err != nil {
		return err
	}

	if archive, err = captureStore.GetObject(ctx, payload.Compression.archiveName()); err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() {
		if err := archive.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close archive")
		}
	}()
	if tarStream, err = newDecoder(payload.Compression, archive); err != nil {
		return err
	}
	defer func() {
		if err := tarStream.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close decoder")
		}
	}()

	if err := s.docker.WriteVolume(ctx, src, progress.NewReader(ctx, tarStream, req.SizeBytes)); err != nil {
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

// Remove deletes the archive the record's payload names and stops: a Storager
// may not delete its own root, so the caller sweeps the directory. An archive
// already gone is not an error.
func (s *Snapshotter) Remove(ctx context.Context, req model.BackendRemoveRequest) error {
	var (
		captureStore storages.Storager
		payload      Payload
		err          error
	)
	if captureStore, err = s.captureStore(req.SnapshotID); err != nil {
		return err
	}
	if payload, err = DecodePayload(req.Payload); err != nil {
		return err
	}
	if err := captureStore.Delete(ctx, payload.Compression.archiveName()); err != nil && !errors.Is(err, storages.ErrFileNotFound) {
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
