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

	"github.com/docker/docker/api/types/image"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// composeImageLabel is the id of the image compose created a container from.
const composeImageLabel = "com.docker.compose.image"

// dockerManifestList is Docker's own media type for a multi-platform index, the older
// twin of ocispec.MediaTypeImageIndex; registries serve either, so both are checked.
const dockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"

// newContainerRefs lists every replica of every service, in service order,
// each with its upstream resolved.
func newContainerRefs(ctx context.Context, introspection model.IntrospectionResult) []model.ContainerRef {
	var refs []model.ContainerRef
	for _, svc := range introspection.Project.Services {
		for _, container := range svc.Containers {
			ref := model.ContainerRef{Service: svc.Name, Replica: container.Number, ID: container.ID, Name: container.Name}
			ref.UpstreamImageID, ref.UpstreamImageRef = resolveUpstream(ctx, introspection, container)
			refs = append(refs, ref)
		}
	}
	return refs
}

// newLayerCaptures returns the replicas whose writable layer capturePolicy takes, each with
// its backend, and the ones it leaves out.
func newLayerCaptures(
	ctx context.Context, containers []model.ContainerRef, capturePolicy model.CapturePolicy,
) ([]model.WritableLayerCaptureRequest, []model.SkippedWritableLayer) {
	var (
		layers  []model.WritableLayerCaptureRequest
		skipped []model.SkippedWritableLayer
	)
	for _, ref := range containers {
		decision := capturePolicy.DecideService(ref.Service).WritableLayer
		if decision.Capture {
			layers = append(layers, model.WritableLayerCaptureRequest{
				Container: ref,
				Backend:   decision.Backend,
			})
			continue
		}
		zerolog.Ctx(ctx).Info().Str("service", ref.Service).Int("replica", ref.Replica).Str("reason", decision.Source).
			Msg("writable layer is not captured; a rollback to this savepoint creates the container fresh from its image")
		skipped = append(skipped, model.SkippedWritableLayer{
			Service:   ref.Service,
			Replica:   ref.Replica,
			Container: ref.Name,
			Reason:    model.SkipReasonExcluded,
			Detail:    decision.Source,
		})
	}
	return layers, skipped
}

// imageCaptureContainers returns the containers whose image capturePolicy takes, and one
// skip per service it leaves out.
func imageCaptureContainers(
	ctx context.Context, containers []model.ContainerRef, capturePolicy model.CapturePolicy,
) ([]model.ContainerRef, []model.SkippedImage) {
	var (
		taken   []model.ContainerRef
		skipped []model.SkippedImage
	)
	// kept are the upstreams some service takes: one is not reported skipped for another.
	kept := map[model.ImageDigestID]bool{}
	reported := map[string]bool{}
	for _, ref := range containers {
		if capturePolicy.DecideService(ref.Service).Image.Capture {
			taken = append(taken, ref)
			kept[ref.UpstreamImageID] = true
		}
	}
	for _, ref := range containers {
		decision := capturePolicy.DecideService(ref.Service).Image
		if decision.Capture || reported[ref.Service] || (ref.UpstreamImageID != "" && kept[ref.UpstreamImageID]) {
			continue
		}
		reported[ref.Service] = true
		zerolog.Ctx(ctx).Info().Str("service", ref.Service).Str("image", ref.UpstreamImageRef).
			Str("reason", decision.Source).Msg("image is not captured")
		skipped = append(skipped, model.SkippedImage{
			Service: ref.Service,
			Image:   ref.UpstreamImageRef,
			Reason:  model.SkipReasonExcluded,
			Detail:  decision.Source,
		})
	}
	return taken, skipped
}

// resolveUpstream is model.ResolveUpstream, with what it cannot say logged:
// a container off the image compose created it on, and a layer naming no upstream.
func resolveUpstream(ctx context.Context, introspection model.IntrospectionResult, container model.Container) (model.ImageDigestID, string) {
	if container.Inspect.Config == nil {
		return "", ""
	}
	warnOnComposeImageMismatch(ctx, introspection, container)

	id, ref, ok := model.ResolveUpstream(introspection, container)
	if !ok {
		zerolog.Ctx(ctx).Info().Str("container", container.ID).Str("image", container.Inspect.Config.Image).
			Msg("the writable layer names no image it was taken over; only the writable layer is captured")
	}
	return id, ref
}

// newUnresolvedImages returns the containers whose upstream resolveUpstream could not name.
func newUnresolvedImages(introspection model.IntrospectionResult, containers []model.ContainerRef) []model.UnresolvedImage {
	images := map[string]string{}
	for _, svc := range introspection.Project.Services {
		for _, container := range svc.Containers {
			if container.Inspect.Config != nil {
				images[container.ID] = container.Inspect.Config.Image
			}
		}
	}
	var unresolved []model.UnresolvedImage
	for _, ref := range containers {
		if ref.UpstreamImageID != "" {
			continue
		}
		unresolved = append(unresolved, model.UnresolvedImage{
			Service: ref.Service, Replica: ref.Replica, Container: ref.Name, Image: images[ref.ID],
		})
	}
	return unresolved
}

// warnOnComposeImageMismatch reports a container whose image is not the one compose
// created it from; the image is still what the capture goes by. Under the containerd
// image store a container names a multi-platform index and compose one manifest in it,
// which tells nothing, so an index is not compared.
func warnOnComposeImageMismatch(ctx context.Context, introspection model.IntrospectionResult, container model.Container) {
	imageID := container.ImageID()
	created, ok := container.Labels[composeImageLabel]
	if !ok || imageID == "" || created == imageID.String() || isImageIndex(introspection.Images[imageID]) {
		return
	}
	zerolog.Ctx(ctx).Warn().Str("container", container.ID).Str("image_id", imageID.String()).
		Str("compose_image", created).Msg("the container runs on another image than compose created it from")
}

// isImageIndex reports whether the daemon describes an image by a multi-platform index,
// in either the OCI or the Docker format. Such an image's id and the one compose
// recorded can name different levels of it, so a mismatch between them means nothing.
func isImageIndex(inspect image.InspectResponse) bool {
	if inspect.Descriptor == nil {
		return false
	}
	switch inspect.Descriptor.MediaType {
	case ocispec.MediaTypeImageIndex, dockerManifestList:
		return true
	}
	return false
}

// newImageCaptures lists the distinct images the containers run on, described from
// the introspected daemon; one the daemon no longer holds is named by reference.
func newImageCaptures(introspection model.IntrospectionResult, containers []model.ContainerRef) []model.ImageCaptureRequest {
	var reqs []model.ImageCaptureRequest
	for _, ref := range containers {
		if ref.UpstreamImageID == "" || slices.ContainsFunc(reqs, func(req model.ImageCaptureRequest) bool {
			return req.Image.ID == ref.UpstreamImageID
		}) {
			continue
		}
		imageToCapture := model.Image{ID: ref.UpstreamImageID}
		if ref.UpstreamImageRef != "" {
			imageToCapture.Refs = []string{ref.UpstreamImageRef}
		}
		inspect, existsInDaemon := introspection.Images[ref.UpstreamImageID]
		if existsInDaemon {
			for _, tag := range inspect.RepoTags {
				if !slices.Contains(imageToCapture.Refs, tag) {
					imageToCapture.Refs = append(imageToCapture.Refs, tag)
				}
			}
			imageToCapture.RepoDigests = inspect.RepoDigests
			imageToCapture.SizeBytes = inspect.Size
		}
		reqs = append(reqs, model.ImageCaptureRequest{
			Image:          imageToCapture,
			ExistsInDaemon: existsInDaemon,
		})
	}
	return reqs
}
