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

package docker

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// CommitContainer snapshots a stopped container's writable layer as a new,
// untagged image carrying comment and labels, and returns its id. The daemon's
// answer is parsed here, the one place an image id enters arcave: what travels on
// is a digest, never a name.
func (e *Engine) CommitContainer(
	ctx context.Context, containerID, comment string, labels map[string]string,
) (model.ImageDigestID, error) {
	// Unpaused: the caller has already stopped the services, and a stopped container
	// cannot be paused.
	resp, err := e.api.ContainerCommit(ctx, containerID, container.CommitOptions{
		Pause:   false,
		Comment: comment,
		Changes: labelChanges(labels),
	})
	if err != nil {
		return "", fmt.Errorf("commit container %s: %w", containerID, err)
	}
	imageID, err := model.ParseImageDigestID(resp.ID)
	if err != nil {
		return "", fmt.Errorf("commit container %s: %w", containerID, err)
	}
	return imageID, nil
}

// labelChanges renders labels as Dockerfile LABEL instructions, in key order so the
// changes are deterministic.
func labelChanges(labels map[string]string) []string {
	keys := slices.Sorted(maps.Keys(labels))
	changes := make([]string, 0, len(keys))
	for _, key := range keys {
		changes = append(changes, "LABEL "+strconv.Quote(key)+"="+strconv.Quote(labels[key]))
	}
	return changes
}

// SaveImage streams one image out of the daemon as a save archive. The caller
// owns the stream and closes it.
func (e *Engine) SaveImage(ctx context.Context, imageID model.ImageDigestID) (io.ReadCloser, error) {
	archive, err := e.api.ImageSave(ctx, []string{imageID.String()})
	if err != nil {
		return nil, fmt.Errorf("save image %s: %w", imageID, err)
	}
	return archive, nil
}

// RemoveImage drops one image from the daemon. Not forced, so an image something
// still references is refused rather than taken.
func (e *Engine) RemoveImage(ctx context.Context, imageID model.ImageDigestID) error {
	if _, err := e.api.ImageRemove(ctx, imageID.String(), image.RemoveOptions{}); err != nil {
		return fmt.Errorf("remove image %s: %w", imageID, err)
	}
	return nil
}

// LoadImage loads one save archive back into the daemon; the response body is read
// to the end, since an unread one can leave the load unfinished. It reports no id:
// the archive was saved off a known image, which the caller confirms with ImageExists.
func (e *Engine) LoadImage(ctx context.Context, r io.Reader) error {
	resp, err := e.api.ImageLoad(ctx, r, client.ImageLoadWithQuiet(true))
	if err != nil {
		return fmt.Errorf("load image archive: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close image load response")
		}
	}()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return fmt.Errorf("read image load response: %w", err)
	}
	return nil
}

// TagImage points ref at an image already in the daemon.
func (e *Engine) TagImage(ctx context.Context, imageID model.ImageDigestID, ref string) error {
	if err := e.api.ImageTag(ctx, imageID.String(), ref); err != nil {
		return fmt.Errorf("tag image %s as %s: %w", imageID, ref, err)
	}
	return nil
}

// ImageRefExists reports whether the daemon holds an image under ref.
func (e *Engine) ImageRefExists(ctx context.Context, ref string) (bool, error) {
	if _, err := e.api.ImageInspect(ctx, ref); err != nil {
		if cerrdefs.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect image %s: %w", ref, err)
	}
	return true, nil
}

// ImageExists reports whether the daemon holds the image. A daemon that answers
// "not found" is not an error — that is the answer. The argument is a digest, so
// this is an identity check and not a lookup by name.
func (e *Engine) ImageExists(ctx context.Context, imageID model.ImageDigestID) (bool, error) {
	if _, err := e.api.ImageInspect(ctx, imageID.String()); err != nil {
		if cerrdefs.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect image %s: %w", imageID, err)
	}
	return true, nil
}

// ListLayerImages lists every arcave/…:sp-<id> tag the daemon holds, with the image it names
// and the origin that image's labels name.
func (e *Engine) ListLayerImages(ctx context.Context) ([]model.TaggedImage, error) {
	summaries, err := e.api.ImageList(ctx, image.ListOptions{
		Filters: filters.NewArgs(filters.Arg("reference", model.RestoredLayerTagNamespace+"/*")),
	})
	if err != nil {
		return nil, fmt.Errorf("list arcave images: %w", err)
	}
	var tagged []model.TaggedImage
	for _, summary := range summaries {
		refs := slices.DeleteFunc(slices.Clone(summary.RepoTags), func(ref string) bool {
			return !model.ImageRef(ref).IsRestoredLayerTag()
		})
		if len(refs) == 0 {
			continue
		}
		// The list leaves out when the image was tagged, which is what a gc's grace reads.
		inspect, err := e.api.ImageInspect(ctx, summary.ID)
		if cerrdefs.IsNotFound(err) {
			continue // removed since the list
		}
		if err != nil {
			return nil, fmt.Errorf("inspect arcave image %s: %w", summary.ID, err)
		}
		origin, _ := model.ParseLayerOrigin(summary.Labels)
		for _, ref := range refs {
			tagged = append(tagged, model.TaggedImage{
				Ref:      model.ImageRef(ref),
				ImageID:  model.ImageDigestID(summary.ID),
				Origin:   origin,
				TaggedAt: inspect.Metadata.LastTagTime,
			})
		}
	}
	slices.SortFunc(tagged, func(a, b model.TaggedImage) int { return strings.Compare(string(a.Ref), string(b.Ref)) })
	return tagged, nil
}

// ListContainerImages lists what every container, running or not, was created from: the
// reference it names and the image that resolved to.
func (e *Engine) ListContainerImages(ctx context.Context) ([]model.ContainerImage, error) {
	containers, err := e.api.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	images := make([]model.ContainerImage, 0, len(containers))
	for _, summary := range containers {
		images = append(images, model.ContainerImage{
			ContainerID: summary.ID,
			Ref:         model.ImageRef(summary.Image),
			ImageID:     model.ImageDigestID(summary.ImageID),
		})
	}
	return images, nil
}
