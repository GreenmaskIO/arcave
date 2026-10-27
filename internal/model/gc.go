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
	"fmt"
	"slices"
	"time"
)

// GarbageDataKind is what a piece of garbage is, in the user's words: what a gc is asked to
// look at, and what its report is grouped by. One or more collectors resolve each.
type GarbageDataKind string

const (
	// GarbageDataKindSavepointData is a savepoint's own directory no record commits: its
	// record, its spec, its streamed volumes and its writable layers' archives and manifests.
	GarbageDataKindSavepointData GarbageDataKind = "savepoint-data"
	// GarbageDataKindDeduplicatedVolumes is volume data shared across savepoints that no
	// savepoint names.
	GarbageDataKindDeduplicatedVolumes GarbageDataKind = "deduplicated-volumes"
	// GarbageDataKindImageLayers is the layers, and configs, of stored images that writable
	// layers and upstream images share, that no stored manifest lists.
	GarbageDataKindImageLayers GarbageDataKind = "image-layers"
	// GarbageDataKindUpstreamImages is a stored upstream image no savepoint names; its layers
	// are image-layers, freed by the gc run after the one that removes it.
	GarbageDataKindUpstreamImages GarbageDataKind = "upstream-images"
	// GarbageDataKindRestoredLayers is a writable layer a rollback restored into the daemon
	// that no container runs on.
	GarbageDataKindRestoredLayers GarbageDataKind = "restored-layers"
)

// GarbageDataKinds lists every data kind.
func GarbageDataKinds() []GarbageDataKind {
	return []GarbageDataKind{
		GarbageDataKindSavepointData, GarbageDataKindDeduplicatedVolumes, GarbageDataKindImageLayers,
		GarbageDataKindUpstreamImages, GarbageDataKindRestoredLayers,
	}
}

// Validate refuses a data kind arcave does not know.
func (m GarbageDataKind) Validate() error {
	if !slices.Contains(GarbageDataKinds(), m) {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidGarbageCollectRequest, string(m))
	}
	return nil
}

// GarbageCollectorKind names one collector: how one implementation finds, and deletes,
// the garbage of its data kind.
type GarbageCollectorKind string

const (
	// GarbageCollectorKindSavepointDirectory reads savepoint directories by their record.
	GarbageCollectorKindSavepointDirectory GarbageCollectorKind = "savepoint-directory"
	// GarbageCollectorKindResticRepositories reads the cdc backend's restic repositories.
	GarbageCollectorKindResticRepositories GarbageCollectorKind = "restic-repositories"
	// GarbageCollectorKindOCIBlobs reads the OCI blob store every stored image manifest lists.
	GarbageCollectorKindOCIBlobs GarbageCollectorKind = "oci-blobs"
	// GarbageCollectorKindOCIImages reads the stored upstream images' OCI manifests.
	GarbageCollectorKindOCIImages GarbageCollectorKind = "oci-images"
	// GarbageCollectorKindDockerLayerTags reads the daemon's arcave/…:sp-<id> tags.
	GarbageCollectorKindDockerLayerTags GarbageCollectorKind = "docker-layer-tags"
)

// GarbageCollectorKinds lists every collector kind.
func GarbageCollectorKinds() []GarbageCollectorKind {
	return []GarbageCollectorKind{
		GarbageCollectorKindSavepointDirectory, GarbageCollectorKindResticRepositories, GarbageCollectorKindOCIBlobs,
		GarbageCollectorKindOCIImages, GarbageCollectorKindDockerLayerTags,
	}
}

// GarbageAction is what a gc that deletes does to one object.
type GarbageAction string

const (
	GarbageActionDeleteObject GarbageAction = "delete-object"
	GarbageActionDeleteTree   GarbageAction = "delete-tree"
	// GarbageActionDeleteImage deletes one stored image, its manifest first.
	GarbageActionDeleteImage GarbageAction = "delete-image"
	// GarbageActionForget is a restic forget of one snapshot, its data freed by the prune after.
	GarbageActionForget GarbageAction = "forget"
	GarbageActionUntag  GarbageAction = "untag"
)

// GarbageReason is the stable code of why an object is dead, or why it was held back.
type GarbageReason string

const (
	// GarbageReasonDeadCapture is a savepoint tree no record commits.
	GarbageReasonDeadCapture  GarbageReason = "dead-capture"
	GarbageReasonUnreferenced GarbageReason = "unreferenced"
	GarbageReasonUnusedTag    GarbageReason = "unused-tag"
	// GarbageReasonBackendUnreadable leaves unjudged what the backend holding it cannot list.
	GarbageReasonBackendUnreadable GarbageReason = "backend-unreadable"
	// GarbageReasonListedByUnreferenced holds back a blob only a manifest no record names
	// lists: it goes once that manifest does.
	GarbageReasonListedByUnreferenced GarbageReason = "listed-by-unreferenced"
	// GarbageReasonRecentlyTagged holds back a layer tag younger than gc.restored_layer_grace.
	GarbageReasonRecentlyTagged GarbageReason = "recently-tagged"
	// GarbageReasonTagTimeUnknown leaves unjudged a layer tag the daemon gives no time for,
	// under a restored layer grace.
	GarbageReasonTagTimeUnknown GarbageReason = "tag-time-unknown"
)

// GarbageCollectRequest is one gc run.
type GarbageCollectRequest struct {
	DryRun bool
	// DataKinds narrow what the gc looks at; empty looks at every one.
	DataKinds []GarbageDataKind
	// RestoredLayerGrace is how old an unused layer tag must be to count as garbage; nil keeps the
	// service's default.
	RestoredLayerGrace *time.Duration
}

// Validate refuses an unknown data kind and a negative restored layer grace.
func (m GarbageCollectRequest) Validate() error {
	for _, dataKind := range m.DataKinds {
		if err := dataKind.Validate(); err != nil {
			return err
		}
	}
	if m.RestoredLayerGrace != nil && *m.RestoredLayerGrace < 0 {
		return fmt.Errorf("%w: negative restored layer grace %s", ErrInvalidGarbageCollectRequest, *m.RestoredLayerGrace)
	}
	return nil
}

// TaggedImage is one tag the daemon holds, with the image it names and that image's origin;
// Origin is zero for an image whose labels name none.
type TaggedImage struct {
	Ref     ImageRef
	ImageID ImageDigestID
	Origin  LayerOrigin
	// TaggedAt is when the daemon last tagged the image; zero when it does not say.
	TaggedAt time.Time
}

// ContainerImage is what one container, running or not, was created from.
type ContainerImage struct {
	ContainerID string
	// Ref is the reference the container was created with.
	Ref     ImageRef
	ImageID ImageDigestID
}

// GarbageStat is one collector's finding over one scope. Every kind reports the same
// summary; the detail behind it is the kind's own type.
type GarbageStat interface {
	Summary() GarbageSummary
	Findings() GarbageFindings
}

// GarbageSummary is what every kind reports alike, so totals add up across kinds.
type GarbageSummary struct {
	Found int `json:"found"`
	// Bytes is what removing the garbage frees; nil when only a backend could say.
	Bytes   *int64 `json:"bytes"`
	Skipped int    `json:"skipped"`
	// Unjudged counts objects the kind could not judge; any makes the gc partial.
	Unjudged int `json:"unjudged"`
	// Aborted is why the kind refused to judge this scope; empty when it judged it.
	Aborted string `json:"aborted,omitempty"`
}

// withBytes is the summary with bytes in place of its own.
func (m GarbageSummary) withBytes(bytes *int64) GarbageSummary {
	m.Bytes = bytes
	return m
}

// GarbageOp is one action a gc that deletes runs.
type GarbageOp struct {
	Action GarbageAction `json:"action"`
	// Target is a key relative to the scope's store, or a daemon reference.
	Target string `json:"target"`
	// Bytes is nil when the size is not known.
	Bytes        *int64        `json:"bytes"`
	Reason       GarbageReason `json:"reason"`
	LastModified time.Time     `json:"last_modified,omitzero"`
	// Savepoint is the dead savepoint whose removal alone frees the target; empty when none.
	Savepoint SavepointID `json:"savepoint,omitempty"`
}

// GarbageSkip is an object that looks dead but is held back.
type GarbageSkip struct {
	Target       string        `json:"target"`
	Reason       GarbageReason `json:"reason"`
	LastModified time.Time     `json:"last_modified,omitzero"`
	// Error says what could not be read, for a skip that is not a verdict.
	Error string `json:"error,omitempty"`
}

// GarbageFindings is the part of a stat every kind shares: what it would do, what it held
// back, what it could not judge, and why it refused, if it did. A kind's stat embeds it.
type GarbageFindings struct {
	// Ops is never nil, so a kind that found nothing reports [] rather than null.
	Ops     []GarbageOp   `json:"ops"`
	Skipped []GarbageSkip `json:"skipped,omitempty"`
	// Unjudged are objects held back because what decides them cannot be read.
	Unjudged []GarbageSkip `json:"unjudged,omitempty"`
	Aborted  string        `json:"aborted,omitempty"`
}

// Findings is the part every kind shares, for a reader holding only the GarbageStat.
func (m GarbageFindings) Findings() GarbageFindings { return m }

// Summary counts the findings; Bytes is nil when any op's size is unknown.
func (m GarbageFindings) Summary() GarbageSummary {
	return GarbageSummary{
		Found:    len(m.Ops),
		Bytes:    m.newOpBytes(),
		Skipped:  len(m.Skipped),
		Unjudged: len(m.Unjudged),
		Aborted:  m.Aborted,
	}
}

// newOpBytes sums the ops' sizes; nil when any op's size is unknown.
func (m GarbageFindings) newOpBytes() *int64 {
	var bytes int64
	for _, op := range m.Ops {
		if op.Bytes == nil {
			return nil
		}
		bytes += *op.Bytes
	}
	return &bytes
}

// SavepointDirectoryStat is a compose project's savepoint directories no record commits.
type SavepointDirectoryStat struct {
	GarbageFindings
	Committed int `json:"committed"`
}

// OCIImageStat is a compose project's stored upstream images no record names.
type OCIImageStat struct {
	GarbageFindings
	Live int `json:"live"`
}

// OCIBlobStat is a compose project's deduplicated blobs no manifest in the store lists.
type OCIBlobStat struct {
	GarbageFindings
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
	Savepoint SavepointID `json:"savepoint,omitempty"`
	Error     string      `json:"error"`
}

// ResticRepositoryStat is a compose project's cdc snapshots whose savepoint is dead. Ops carry no
// bytes: what forgetting them frees is estimated per repository, by a prune dry run.
type ResticRepositoryStat struct {
	GarbageFindings
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
func (m ResticRepositoryStat) Summary() GarbageSummary {
	return m.GarbageFindings.Summary().withBytes(m.newEstimateBytes())
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

// DockerLayerTagStat is the daemon's arcave/…:sp-<id> tags no container runs on, of the layers the
// arcave project committed for the session's compose projects.
type DockerLayerTagStat struct {
	GarbageFindings
	InUse int `json:"in_use"`
	// Unattributed counts tags whose image names no origin: no gc judges them.
	Unattributed int `json:"unattributed"`
	// OtherComposeProjects counts the arcave project's tags of compose projects this session
	// does not hold; this gc does not judge them.
	OtherComposeProjects int `json:"other_compose_projects"`
}

// GarbageReport is one collector's stat over one scope, with the summary lifted out.
type GarbageReport struct {
	DataKind GarbageDataKind `json:"data_kind"`
	// Collector is the collector that found it.
	Collector GarbageCollectorKind `json:"collector"`
	// ComposeProject is empty for a daemon-level collector.
	ComposeProject DockerComposeProjectName `json:"compose_project,omitempty"`
	Summary        GarbageSummary           `json:"summary"`
	// Detail is the kind's own stat; nil when the collector failed.
	Detail GarbageStat `json:"detail"`
	Error  string      `json:"error,omitempty"`
	// Deletion is what deleting Detail's ops did; nil in a dry run, for a collector that only
	// reports, and for a report that failed or was aborted.
	Deletion *GarbageDeletion `json:"deletion,omitempty"`
}

// GarbageDeletion is what deleting one report's ops did.
type GarbageDeletion struct {
	Deleted []GarbageDeleted       `json:"deleted,omitempty"`
	Failed  []GarbageDeleteFailure `json:"failed,omitempty"`
	// NotRun counts the ops no deletion was started for: the run was cancelled or lost its lock.
	NotRun int `json:"not_run"`
	// Reclaimed is what deletions freed beyond their ops' own sizes: a restic prune per repository.
	Reclaimed []GarbageReclaimed `json:"reclaimed,omitempty"`
}

// FreedBytes sums the deleted ops' sizes and what was reclaimed; nil when any is unknown.
func (m GarbageDeletion) FreedBytes() *int64 {
	var bytes int64
	for _, deleted := range m.Deleted {
		if deleted.Bytes == nil {
			return nil
		}
		bytes += *deleted.Bytes
	}
	for _, reclaimed := range m.Reclaimed {
		if reclaimed.Bytes == nil {
			return nil
		}
		bytes += *reclaimed.Bytes
	}
	return &bytes
}

// GarbageReclaimed is what one deletion over several ops freed, a restic repository's prune.
type GarbageReclaimed struct {
	Target string `json:"target"`
	// Bytes is nil when not known.
	Bytes *int64 `json:"bytes"`
}

// GarbageDeleted is one op's target, deleted or found already gone.
type GarbageDeleted struct {
	Target string `json:"target"`
	// Bytes is what deleting it freed: the op's, 0 for one already gone, nil when not known.
	Bytes *int64 `json:"bytes"`
}

// GarbageDeleteFailure is one op's target the deletion failed on.
type GarbageDeleteFailure struct {
	Target string `json:"target"`
	Error  string `json:"error"`
}

// GarbageTotal adds up every report; its deletion counts stay zero in a dry run.
type GarbageTotal struct {
	Found         int   `json:"found"`
	Bytes         int64 `json:"bytes"`
	BytesComplete bool  `json:"bytes_complete"`
	Skipped       int   `json:"skipped"`
	Unjudged      int   `json:"unjudged"`
	Aborted       int   `json:"aborted"`
	Failed        int   `json:"failed"`
	Deleted       int   `json:"deleted,omitempty"`
	DeleteFailed  int   `json:"delete_failed,omitempty"`
	NotRun        int   `json:"not_run,omitempty"`
	// FreedBytes is what the deletions freed; nil in a dry run, or when any deleted size is unknown.
	FreedBytes *int64 `json:"freed_bytes,omitempty"`
}

// DeadSavepoint is what one dead savepoint alone frees: every op naming it, its tree and the blobs
// only its manifests list. Its images are never attributed; its restic snapshots carry no bytes.
type DeadSavepoint struct {
	ComposeProject DockerComposeProjectName `json:"compose_project"`
	Savepoint      SavepointID              `json:"savepoint"`
	Found          int                      `json:"found"`
	// Bytes is nil when any op's size is unknown.
	Bytes *int64 `json:"bytes"`
}

// GarbageCollectResult is a gc's whole report over one session.
type GarbageCollectResult struct {
	DryRun  bool            `json:"dry_run"`
	Session SessionID       `json:"session"`
	Reports []GarbageReport `json:"reports"`
	// Savepoints groups the ops naming a dead savepoint by it, in report order.
	Savepoints []DeadSavepoint `json:"savepoints,omitempty"`
	Total      GarbageTotal    `json:"total"`
	Warnings   []string        `json:"warnings,omitempty"`
}

// IsPartial reports whether anything was left unjudged, aborted, failed or not deleted.
func (m GarbageCollectResult) IsPartial() bool {
	total := m.Total
	return total.Failed > 0 || total.Aborted > 0 || total.Unjudged > 0 || total.DeleteFailed > 0 || total.NotRun > 0
}
