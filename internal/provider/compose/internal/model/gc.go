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

package model

import (
	"github.com/greenmaskio/arcave/internal/model"
)

// TaggedImage is one tag the daemon holds, with the image it names and that image's origin;
// Origin is zero for an image whose labels name none.
type TaggedImage struct {
	Ref     model.ImageRef
	ImageID ImageDigestID
	Origin  LayerOrigin
}

// ContainerImage is what one container, running or not, was created from.
type ContainerImage struct {
	ContainerID string
	// Ref is the reference the container was created with.
	Ref     model.ImageRef
	ImageID ImageDigestID
}

// The compose provider's data kinds, in the order its reports come.
const (
	// GarbageDataKindSavepointData is a savepoint's own directory no record commits: its
	// record, its spec, its streamed volumes and its writable layers' archives and manifests.
	GarbageDataKindSavepointData model.GarbageDataKind = "savepoint-data"
	// GarbageDataKindDeduplicatedVolumes is volume data shared across savepoints that no
	// savepoint names.
	GarbageDataKindDeduplicatedVolumes model.GarbageDataKind = "deduplicated-volumes"
	// GarbageDataKindImageLayers is the layers, and configs, of stored images that writable
	// layers and upstream images share, that no stored manifest lists.
	GarbageDataKindImageLayers model.GarbageDataKind = "image-layers"
	// GarbageDataKindUpstreamImages is a stored upstream image no savepoint names; its layers
	// are image-layers, freed by the gc run after the one that removes it.
	GarbageDataKindUpstreamImages model.GarbageDataKind = "upstream-images"
	// GarbageDataKindRestoredLayers is a writable layer a rollback restored into the daemon
	// that no container runs on.
	GarbageDataKindRestoredLayers model.GarbageDataKind = "restored-layers"
)

// The compose provider's collectors.
const (
	// GarbageCollectorKindSavepointDirectory reads savepoint directories by their record.
	GarbageCollectorKindSavepointDirectory model.GarbageCollectorKind = "savepoint-directory"
	// GarbageCollectorKindResticRepositories reads the cdc backend's restic repositories.
	GarbageCollectorKindResticRepositories model.GarbageCollectorKind = "restic-repositories"
	// GarbageCollectorKindOCIBlobs reads the OCI blob store every stored image manifest lists.
	GarbageCollectorKindOCIBlobs model.GarbageCollectorKind = "oci-blobs"
	// GarbageCollectorKindOCIImages reads the stored upstream images' OCI manifests.
	GarbageCollectorKindOCIImages model.GarbageCollectorKind = "oci-images"
	// GarbageCollectorKindDockerLayerTags reads the daemon's arcave/…:sp-<savepoint> tags.
	GarbageCollectorKindDockerLayerTags model.GarbageCollectorKind = "docker-layer-tags"
)

// What the compose provider's collectors do to an object.
const (
	GarbageActionDeleteObject model.GarbageAction = "delete-object"
	GarbageActionDeleteTree   model.GarbageAction = "delete-tree"
	// GarbageActionDeleteImage deletes one stored image, its manifest first.
	GarbageActionDeleteImage model.GarbageAction = "delete-image"
	// GarbageActionForget is a restic forget of one snapshot, its data freed by the prune after;
	// of a repository, its prune alone.
	GarbageActionForget model.GarbageAction = "forget"
	GarbageActionUntag  model.GarbageAction = "untag"
)

// Why the compose provider's collectors judge an object dead, or hold it back.
const (
	// GarbageReasonDeadCapture is a savepoint tree no record commits.
	GarbageReasonDeadCapture  model.GarbageReason = "dead-capture"
	GarbageReasonUnreferenced model.GarbageReason = "unreferenced"
	GarbageReasonUnusedTag    model.GarbageReason = "unused-tag"
	// GarbageReasonUnprunedData is a restic repository holding data no snapshot uses: a prune
	// that failed after its forget, or a capture cut short.
	GarbageReasonUnprunedData model.GarbageReason = "unpruned-data"
	// GarbageReasonBackendUnreadable leaves unjudged what the backend holding it cannot list.
	GarbageReasonBackendUnreadable model.GarbageReason = "backend-unreadable"
	// GarbageReasonListedByUnreferenced holds back a blob only a manifest no record names
	// lists: it goes once that manifest does.
	GarbageReasonListedByUnreferenced model.GarbageReason = "listed-by-unreferenced"
)

// GarbageDataKinds lists every data kind the compose provider finds garbage of, in report order.
func GarbageDataKinds() []model.GarbageDataKind {
	return []model.GarbageDataKind{
		GarbageDataKindSavepointData, GarbageDataKindDeduplicatedVolumes, GarbageDataKindImageLayers,
		GarbageDataKindUpstreamImages, GarbageDataKindRestoredLayers,
	}
}

// GarbageCollectorKinds lists every collector of the compose provider.
func GarbageCollectorKinds() []model.GarbageCollectorKind {
	return []model.GarbageCollectorKind{
		GarbageCollectorKindSavepointDirectory, GarbageCollectorKindResticRepositories, GarbageCollectorKindOCIBlobs,
		GarbageCollectorKindOCIImages, GarbageCollectorKindDockerLayerTags,
	}
}

// SavepointDirectoryStat is a compose project's savepoint directories no record commits.
type SavepointDirectoryStat struct {
	model.GarbageFindings
	Committed int `json:"committed"`
}

// OCIImageStat is a compose project's stored upstream images no record names.
type OCIImageStat struct {
	model.GarbageFindings
	Live int `json:"live"`
}

// OCIBlobStat is a compose project's deduplicated blobs no manifest in the store lists.
type OCIBlobStat struct {
	model.GarbageFindings
	Live int `json:"live"`
	// Unreadable are the manifests that cannot be read; any one of them aborts the kind,
	// since an unread manifest references everything.
	Unreadable []UnreadableManifest `json:"unreadable,omitempty"`
}

// UnreadableManifest is a manifest in the store a gc could not read.
type UnreadableManifest struct {
	// Key is the manifest, relative to the compose project store.
	Key string `json:"key"`
	// Savepoint is the committed savepoint naming it; empty for one no record names.
	Savepoint model.SavepointID `json:"savepoint,omitempty"`
	Error     string            `json:"error"`
}

// ResticRepositoryStat is a compose project's cdc snapshots whose savepoint is dead. Ops carry no
// bytes: what forgetting them frees is estimated per repository, by a prune dry run.
type ResticRepositoryStat struct {
	model.GarbageFindings
	LiveRepos     int `json:"live_repos"`
	LiveSnapshots int `json:"live_snapshots"`
	// Unattributed counts snapshots tagged with no savepoint: no gc judges them.
	Unattributed int                    `json:"unattributed"`
	Estimates    []ResticForgetEstimate `json:"estimates,omitempty"`
}

// ResticForgetEstimate is what forgetting Snapshots, then pruning, frees in one repository.
type ResticForgetEstimate struct {
	Repo      string   `json:"repo"`
	Snapshots []string `json:"snapshots"`
	// FreedBytes is nil when restic gave no estimate.
	FreedBytes *int64 `json:"freed_bytes"`
}

// Summary counts the findings; Bytes is the sum of the estimates, nil when any is unknown.
func (m ResticRepositoryStat) Summary() model.GarbageSummary {
	return m.GarbageFindings.Summary().WithBytes(m.newEstimateBytes())
}

// newEstimateBytes sums the estimates; nil when any is unknown.
func (m ResticRepositoryStat) newEstimateBytes() *int64 {
	var bytes int64
	for _, estimate := range m.Estimates {
		if estimate.FreedBytes == nil {
			return nil
		}
		bytes += *estimate.FreedBytes
	}
	return &bytes
}

// DockerLayerTagStat is the daemon's arcave/…:sp-<savepoint> tags no container runs on, of the layers the
// arcave project committed for the session's compose projects.
type DockerLayerTagStat struct {
	model.GarbageFindings
	InUse int `json:"in_use"`
	// Unattributed counts tags whose image names no origin: no gc judges them.
	Unattributed int `json:"unattributed"`
	// OtherComposeProjects counts the arcave project's tags of compose projects this session
	// does not hold; this gc does not judge them.
	OtherComposeProjects int `json:"other_compose_projects"`
}
