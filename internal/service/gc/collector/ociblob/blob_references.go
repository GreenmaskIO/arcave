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

package ociblob

import (
	"context"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy"
	"github.com/greenmaskio/arcave/internal/service/gc"
)

// blobReferences is which image blobs the manifests under a compose project store list, by who
// names each manifest.
type blobReferences struct {
	// bySavepoints are the blob digests, in hex, a manifest some committed record names lists.
	bySavepoints map[string]bool
	// byUnnamed are the blob digests only stored images no record names list; an intact manifest
	// stops a capture writing its blobs, so they are freed only by the run after it goes.
	byUnnamed map[string]bool
	// byDeadSavepoints maps a blob digest to the dead savepoint trees whose manifests list it;
	// those hold nothing, and only name what each one's removal frees.
	byDeadSavepoints map[string][]model.SavepointID
	// unreadable are the manifests that could not be read; while any is, the references are not
	// whole and no blob may be judged unreferenced by them.
	unreadable []model.UnreadableManifest
}

// manifestRef is one manifest readBlobReferences reads, by its capture's key: savepoint is the
// record naming it, dead the dead tree holding it, or else image and size the unreferenced image.
type manifestRef struct {
	key       string
	savepoint model.SavepointID
	image     model.ImageDigestID
	dead      model.SavepointID
	size      int64
}

// listedDigests is what reading one manifest returned.
type listedDigests struct {
	digests []string
	err     error
}

// readBlobReferences reads every manifest the scope lists, at most concurrency at once; a dead
// tree's manifests only attribute their blobs.
func readBlobReferences(
	ctx context.Context, scope gc.ComposeProjectScope, concurrency int,
) (blobReferences, error) {
	var (
		live                 = gc.NewLiveImages(scope.Savepoints)
		liveRefs, unreadable = newLiveRefs(scope.Savepoints, live)
		refs                 = append(liveRefs, newUnreferencedRefs(scope, live)...)
		listed               = make([]listedDigests, len(refs))
		wg                   sync.WaitGroup
		slots                = make(chan struct{}, max(concurrency, 1))
	)
	// No read fails the others: each one's error is the references', as an unreadable manifest.
	for i, ref := range refs {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			digests, err := readListedDigests(ctx, scope.Store, ref)
			listed[i] = listedDigests{
				digests: digests,
				err:     err,
			}
		})
	}
	wg.Wait()
	// A read cancelled midway reports unreadable: the references of a cancelled run are no verdict.
	if err := ctx.Err(); err != nil {
		return blobReferences{}, err
	}
	return newBlobReferences(refs, listed, unreadable), nil
}

// newLiveRefs names the manifest of every writable layer and live image the savepoints record;
// one whose key cannot be built is returned unreadable.
func newLiveRefs(
	savepoints []model.Savepoint, live map[model.ImageDigestID]model.SavepointID,
) ([]manifestRef, []model.UnreadableManifest) {
	var (
		refs       []manifestRef
		unreadable []model.UnreadableManifest
	)
	for _, savepoint := range savepoints {
		for _, layer := range savepoint.WritableLayers {
			key, err := imagecopy.WritableLayerKey(savepoint.ID, layer.Service, layer.Replica)
			if err != nil {
				unreadable = append(unreadable, newUnreadableManifest(manifestRef{
					key:       path.Join(savepoint.ID.String(), layer.Service),
					savepoint: savepoint.ID,
				}, err))
				continue
			}
			refs = append(refs, manifestRef{
				key:       key,
				savepoint: savepoint.ID,
			})
		}
	}
	for _, imageID := range slices.Sorted(maps.Keys(live)) {
		key, err := imagecopy.ImageKey(imageID)
		if err != nil {
			unreadable = append(unreadable, newUnreadableManifest(manifestRef{
				key:       imageID.String(),
				savepoint: live[imageID],
			}, err))
			continue
		}
		refs = append(refs, manifestRef{
			key:       key,
			savepoint: live[imageID],
			image:     imageID,
		})
	}
	return refs, unreadable
}

// newUnreferencedRefs names the manifests the scope lists that no record does: every stored image
// no record names, and every dead tree's layer.
func newUnreferencedRefs(
	scope gc.ComposeProjectScope, live map[model.ImageDigestID]model.SavepointID,
) []manifestRef {
	var refs []manifestRef
	for _, image := range imagecopy.NewStoredImages(scope.Objects) {
		if _, isLive := live[image.ID]; !isLive && image.HasManifest {
			refs = append(refs, manifestRef{
				key:   image.Key,
				image: image.ID,
				size:  image.ManifestBytes,
			})
		}
	}
	for _, manifest := range imagecopy.NewLayerManifests(scope.Objects) {
		if slices.Contains(scope.Dead, manifest.Savepoint) {
			refs = append(refs, manifestRef{
				key:  manifest.Key,
				dead: manifest.Savepoint,
			})
		}
	}
	return refs
}

// readListedDigests returns the blobs the manifest ref names lists: an unreferenced image's as a
// capture of it would reuse them, any other's as listed.
func readListedDigests(ctx context.Context, store storages.Navigator, ref manifestRef) ([]string, error) {
	if ref.savepoint == "" && ref.dead == "" {
		return imagecopy.ReadReusableBlobs(ctx, store, ref.key, ref.size, ref.image)
	}
	return imagecopy.ReadListedBlobs(ctx, store, ref.key)
}

// newBlobReferences sorts the blobs each manifest of refs lists, as listed reads them, by who
// names the manifest. A dead tree's manifest only attributes its blobs: one it cannot read keeps
// nothing alive.
func newBlobReferences(
	refs []manifestRef, listed []listedDigests, unreadable []model.UnreadableManifest,
) blobReferences {
	var (
		bySavepoints     = map[string]bool{}
		byUnnamed        = map[string]bool{}
		byDeadSavepoints = map[string][]model.SavepointID{}
	)
	for i, ref := range refs {
		switch digests, err := listed[i].digests, listed[i].err; {
		case ref.dead != "":
			for _, digest := range digests {
				if !slices.Contains(byDeadSavepoints[digest], ref.dead) {
					byDeadSavepoints[digest] = append(byDeadSavepoints[digest], ref.dead)
				}
			}
		case err != nil:
			unreadable = append(unreadable, newUnreadableManifest(ref, err))
		case ref.savepoint != "":
			for _, digest := range digests {
				bySavepoints[digest] = true
			}
		default:
			for _, digest := range digests {
				byUnnamed[digest] = true
			}
		}
	}
	slices.SortFunc(unreadable, func(a, b model.UnreadableManifest) int { return strings.Compare(a.Key, b.Key) })
	for _, ids := range byDeadSavepoints {
		slices.Sort(ids)
	}
	return blobReferences{
		bySavepoints:     bySavepoints,
		byUnnamed:        byUnnamed,
		byDeadSavepoints: byDeadSavepoints,
		unreadable:       unreadable,
	}
}

func newUnreadableManifest(ref manifestRef, err error) model.UnreadableManifest {
	return model.UnreadableManifest{
		Key:       imagecopy.ManifestKey(ref.key),
		Savepoint: ref.savepoint,
		Error:     err.Error(),
	}
}
