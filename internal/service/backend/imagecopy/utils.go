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

package imagecopy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
)

// WriteManifest commits a capture's manifest. It goes last: the manifest is the
// capture's commit marker, so one that failed halfway leaves none.
func WriteManifest(ctx context.Context, imageStore storages.Storager, manifest Manifest) error {
	body, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", manifestName, err)
	}
	if err := imageStore.PutObject(ctx, manifestName, strings.NewReader(string(body))); err != nil {
		return fmt.Errorf("write %s: %w", manifestName, err)
	}
	return nil
}

// intactManifest reports whether the store holds a whole capture of imageID: a
// manifest that decodes, names that image and lists members. Written last, it
// vouches for everything before it; a torn one does not, and the capture is
// written again.
func intactManifest(ctx context.Context, imageStore storages.Storager, imageID model.ImageDigestID) (bool, error) {
	src, err := imageStore.GetObject(ctx, manifestName)
	if errors.Is(err, storages.ErrFileNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", manifestName, err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close image manifest")
		}
	}()

	var manifest Manifest
	if err := json.NewDecoder(src).Decode(&manifest); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("the stored image manifest does not decode; the image is stored again")
		return false, nil
	}
	return manifest.ImageID == imageID && len(manifest.Members) > 0, nil
}

// ReadManifest reads a capture's manifest back and refuses one written in a form
// the caller does not handle.
func ReadManifest(ctx context.Context, imageStore storages.Storager, format Format) (Manifest, error) {
	src, err := imageStore.GetObject(ctx, manifestName)
	if err != nil {
		if errors.Is(err, storages.ErrFileNotFound) {
			return Manifest{}, fmt.Errorf("%w: %s: %w", model.ErrSavepointImageMissing, manifestName, err)
		}
		return Manifest{}, fmt.Errorf("read %s: %w", manifestName, err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close image manifest")
		}
	}()

	var manifest Manifest
	if err := json.NewDecoder(src).Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode %s: %w", manifestName, err)
	}
	if manifest.Format != format {
		return Manifest{}, fmt.Errorf("%w: the capture was stored as %q, this backend reads %q",
			model.ErrUnsupportedImageArchive, manifest.Format, format)
	}
	if len(manifest.Members) == 0 {
		return Manifest{}, fmt.Errorf("%w: %s names no members", model.ErrSavepointImageMissing, manifestName)
	}
	return manifest, nil
}
