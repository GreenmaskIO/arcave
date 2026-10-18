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

// Package fullcopy keeps a container's committed image as the archive the daemon
// handed over: one object per capture, nothing shared, nothing addressable.
//
// It is the archiver that costs nothing to reason about, and the only one a daemon
// too old to export an addressable archive can use.
package fullcopy

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy"
	"github.com/greenmaskio/arcave/internal/utils"
)

// archiveName is the whole save archive, stored verbatim.
const archiveName = "image.tar"

// Archiver writes one archive per capture and reads it straight back.
type Archiver struct{}

// New returns the archiver. It holds nothing: the store it writes to and the archive
// it writes come per call.
func New() *Archiver { return &Archiver{} }

// Store writes the archive whole. Nothing is deduplicated, so every byte written is
// a byte this capture added.
func (s *Archiver) Store(
	ctx context.Context, dst storages.Writer, imageID model.ImageDigestID, archive io.Reader,
) (imagecopy.StoredSize, error) {
	counted := utils.NewCountingReader(archive)
	if err := dst.PutObject(ctx, archiveName, counted); err != nil {
		return imagecopy.StoredSize{}, fmt.Errorf("write image archive: %w", err)
	}

	if err := imagecopy.WriteManifest(ctx, dst, imagecopy.Manifest{
		ImageID: imageID,
		Format:  imagecopy.FormatArchive,
		Members: []imagecopy.Member{{Name: archiveName, Kind: imagecopy.KindArchive}},
	}); err != nil {
		return imagecopy.StoredSize{}, err
	}
	return imagecopy.StoredSize{Image: counted.N(), Written: counted.N()}, nil
}

// Load streams the stored archive back out.
func (s *Archiver) Load(
	ctx context.Context, src storages.Storager, imageID model.ImageDigestID, archive io.Writer,
) error {
	manifest, err := imagecopy.ReadManifest(ctx, src, imagecopy.FormatArchive)
	if err != nil {
		return err
	}
	if manifest.ImageID != imageID {
		return fmt.Errorf("%w: the capture holds image %s, the record names %s",
			model.ErrSavepointImageMissing, manifest.ImageID, imageID)
	}

	stored, err := src.GetObject(ctx, archiveName)
	if err != nil {
		if errors.Is(err, storages.ErrFileNotFound) {
			return fmt.Errorf("%w: image %s: %w", model.ErrSavepointImageMissing, imageID, err)
		}
		return fmt.Errorf("read image archive: %w", err)
	}
	defer func() {
		if err := stored.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close image archive stream")
		}
	}()

	if _, err := io.Copy(archive, stored); err != nil {
		return fmt.Errorf("stream image archive: %w", err)
	}
	return nil
}
