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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
)

// StoredImage is one image directory under the shared image store.
type StoredImage struct {
	// ID is read off the directory's name, sha256:<hex>; a stray directory is listed too.
	ID model.ImageDigestID
	// Key is the directory, relative to the compose project store.
	Key          string
	Bytes        int64
	LastModified time.Time
	// ManifestBytes is the size of the directory's manifest; HasManifest is false without one.
	ManifestBytes int64
	HasManifest   bool
}

// NewStoredImages groups the image directories out of a listing of a compose project store,
// with each one's size, newest write and manifest.
func NewStoredImages(objects []storages.ObjectStat) []StoredImage {
	// <namespace>/<algorithm>/<hex>/<member>
	byDir := map[string][]storages.ObjectStat{}
	for _, object := range objects {
		rest, ok := strings.CutPrefix(object.Name, namespaceUpstream+"/")
		parts := strings.SplitN(rest, "/", 3)
		if !ok || len(parts) < 3 {
			continue
		}
		dir := path.Join(parts[0], parts[1])
		byDir[dir] = append(byDir[dir], object)
	}

	images := make([]StoredImage, 0, len(byDir))
	for dir, members := range byDir {
		var (
			algorithm, hex, _   = strings.Cut(dir, "/")
			key                 = path.Join(namespaceUpstream, dir)
			bytes, lastModified = utils.MeasureObjects(members)
			manifestBytes       int64
			hasManifest         bool
		)
		for _, member := range members {
			if member.Name == ManifestKey(key) {
				manifestBytes, hasManifest = member.Size, true
			}
		}
		images = append(images, StoredImage{
			ID:            model.ImageDigestID(algorithm + ":" + hex),
			Key:           key,
			Bytes:         bytes,
			LastModified:  lastModified,
			ManifestBytes: manifestBytes,
			HasManifest:   hasManifest,
		})
	}
	slices.SortFunc(images, func(a, b StoredImage) int { return strings.Compare(a.Key, b.Key) })
	return images
}

// LayerManifest is one writable layer's manifest in a listing of a compose project store.
type LayerManifest struct {
	// Savepoint is the tree the layer lies in, read off the key; it may fail the gate.
	Savepoint model.SavepointID
	// Key is the layer's directory, relative to the compose project store.
	Key string
	// Size is the manifest's size, as listed.
	Size int64
}

// NewLayerManifests lists the writable layers' manifests out of a listing of a compose project
// store.
func NewLayerManifests(objects []storages.ObjectStat) []LayerManifest {
	var manifests []LayerManifest
	for _, object := range objects {
		// <savepoint-id>/images/…/<manifest>
		id, rest, _ := strings.Cut(object.Name, "/")
		if strings.HasPrefix(rest, namespaceImages+"/") && path.Base(rest) == manifestName {
			manifests = append(manifests, LayerManifest{
				Savepoint: model.SavepointID(id),
				Key:       path.Dir(object.Name),
				Size:      object.Size,
			})
		}
	}
	return manifests
}

// ManifestKey is the manifest of the capture at key.
func ManifestKey(key string) string {
	return path.Join(key, manifestName)
}

// ReadListedBlobs returns the blobs the manifest of the capture at key lists; one that is
// missing, does not decode or names no members is an error.
func ReadListedBlobs(ctx context.Context, store storages.Navigator, key string) ([]string, error) {
	captureStore, err := store.SubStorage(key, true)
	if err != nil {
		return nil, fmt.Errorf("re-root store at %s: %w", key, err)
	}
	manifest, err := readManifest(ctx, captureStore)
	if err != nil {
		return nil, err
	}
	return newBlobDigests(manifest), nil
}

// ReadReusableBlobs returns the blobs a capture of image reuses off the stored manifest at key,
// listed as size bytes: none when the capture would store the image again. A read short of size is
// an error: an intact manifest cut off on the way reads the same.
func ReadReusableBlobs(
	ctx context.Context, store storages.Navigator, key string, size int64, image model.ImageDigestID,
) ([]string, error) {
	imageStore, err := store.SubStorage(key, true)
	if err != nil {
		return nil, fmt.Errorf("re-root store at %s: %w", key, err)
	}
	src, err := imageStore.GetObject(ctx, manifestName)
	if errors.Is(err, storages.ErrFileNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", manifestName, err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close image manifest")
		}
	}()
	body, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", manifestName, err)
	}
	if int64(len(body)) != size {
		return nil, fmt.Errorf("read %s: %d of the %d bytes listed", manifestName, len(body), size)
	}
	// One that does not decode is stored again by the next capture: it holds nothing.
	manifest, intact, err := parseIntactManifest(bytes.NewReader(body), image)
	if err != nil || !intact {
		return nil, nil
	}
	return newBlobDigests(manifest), nil
}

// newBlobDigests lists the blobs a manifest names.
func newBlobDigests(manifest Manifest) []string {
	var digests []string
	for _, member := range manifest.Members {
		if member.Kind == KindBlob {
			digests = append(digests, member.Digest)
		}
	}
	return digests
}
