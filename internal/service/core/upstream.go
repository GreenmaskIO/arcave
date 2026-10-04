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

package core

import (
	"context"
	"slices"
	"strings"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// composeImageLabel is the id of the image compose created a container from.
const composeImageLabel = "com.docker.compose.image"

// capturableContainers lists every replica of every service, in service order,
// each with its upstream resolved.
func capturableContainers(ctx context.Context, res model.IntrospectionResult) []model.ContainerRef {
	var refs []model.ContainerRef
	for _, svc := range res.Project.Services {
		for _, c := range svc.Containers {
			ref := model.ContainerRef{Service: svc.Name, Replica: c.Number, ID: c.ID}
			ref.UpstreamImageID, ref.UpstreamImageRef = resolveUpstream(ctx, res, c)
			refs = append(refs, ref)
		}
	}
	return refs
}

// resolveUpstream is the image c's compose file named: the container's own image
// while it runs on it, and what the layer's labels name once it runs on an arcave
// tag. Empty for a layer carrying no labels.
func resolveUpstream(ctx context.Context, res model.IntrospectionResult, c model.Container) (model.ImageDigestID, string) {
	if c.Inspect.Config == nil {
		return "", ""
	}
	name := c.Inspect.Config.Image
	var imageID model.ImageDigestID
	if c.Inspect.ContainerJSONBase != nil {
		// Introspection has already gated the id; one it could not parse stays empty.
		if id, err := model.ParseImageDigestID(c.Inspect.Image); err == nil {
			imageID = id
		}
	}
	warnOnComposeImageMismatch(ctx, c, imageID)

	if !isLayerRef(name) {
		return imageID, name
	}
	if id, ref, ok := labelledUpstream(res, imageID); ok {
		return id, ref
	}
	zerolog.Ctx(ctx).Warn().Str("container", c.ID).Str("image", name).
		Msg("the layer names no upstream image; only the layer is captured")
	return "", ""
}

// labelledUpstream is the upstream a committed layer's labels name. Only the id
// label is needed: the image it names may be gone from the daemon.
func labelledUpstream(res model.IntrospectionResult, imageID model.ImageDigestID) (model.ImageDigestID, string, bool) {
	inspect, ok := res.Images[imageID]
	if !ok || inspect.Config == nil {
		return "", "", false
	}
	labels := inspect.Config.Labels
	id, err := model.ParseImageDigestID(labels[model.BaseImageIDLabel])
	if err != nil {
		return "", "", false
	}
	return id, labels[model.BaseImageRefLabel], true
}

// warnOnComposeImageMismatch reports a container whose image is not the one compose
// created it from; the image is still what the capture goes by.
func warnOnComposeImageMismatch(ctx context.Context, c model.Container, imageID model.ImageDigestID) {
	created, ok := c.Labels[composeImageLabel]
	if !ok || imageID == "" || created == imageID.String() {
		return
	}
	zerolog.Ctx(ctx).Warn().Str("container", c.ID).Str("image_id", imageID.String()).
		Str("compose_image", created).Msg("the container runs on another image than compose created it from")
}

// isLayerRef reports whether ref is a tag a rollback put on a restored layer:
// arcave/…:sp-<savepoint id>.
func isLayerRef(ref string) bool {
	if !strings.HasPrefix(ref, restoredImageNamespace+"/") {
		return false
	}
	idx := strings.LastIndex(ref, ":")
	if idx < 0 {
		return false
	}
	id, ok := strings.CutPrefix(ref[idx+1:], restoredTagPrefix)
	return ok && model.SavepointID(id).Validate() == nil
}

// upstreamImages lists the distinct upstreams the containers name, described from
// the introspected daemon; one the daemon no longer holds is named by reference.
func upstreamImages(res model.IntrospectionResult, containers []model.ContainerRef) []model.UpstreamImageCaptureRequest {
	var reqs []model.UpstreamImageCaptureRequest
	for _, c := range containers {
		if c.UpstreamImageID == "" || slices.ContainsFunc(reqs, func(req model.UpstreamImageCaptureRequest) bool {
			return req.Image.ID == c.UpstreamImageID
		}) {
			continue
		}
		image := model.UpstreamImage{ID: c.UpstreamImageID}
		if c.UpstreamImageRef != "" {
			image.Refs = []string{c.UpstreamImageRef}
		}
		inspect, inDaemon := res.Images[c.UpstreamImageID]
		if inDaemon {
			for _, tag := range inspect.RepoTags {
				if !slices.Contains(image.Refs, tag) {
					image.Refs = append(image.Refs, tag)
				}
			}
			image.RepoDigests = inspect.RepoDigests
			image.SizeBytes = inspect.Size
		}
		reqs = append(reqs, model.UpstreamImageCaptureRequest{Image: image, InDaemon: inDaemon})
	}
	return reqs
}
