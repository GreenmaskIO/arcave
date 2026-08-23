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
