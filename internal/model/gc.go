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
	"slices"
	"time"
)

// GarbageDataKind is what a piece of garbage is, in the user's words: what a gc is asked to
// look at, and what its report is grouped by. One or more collectors resolve each.
type GarbageDataKind string

const (
	// GarbageDataKindSafetySnapshots is a compose project's committed safety snapshots beyond the
	// newest gc.safety_snapshots.retain_recent; what only they hold goes on the next run.
	GarbageDataKindSafetySnapshots GarbageDataKind = "safety-snapshots"
)

// GarbageCollectorKind names one collector: how one implementation finds, and deletes,
// the garbage of its data kind.
type GarbageCollectorKind string

const (
	// GarbageCollectorKindSafetySnapshotRetention reads the committed safety snapshots against the
	// configured retention.
	GarbageCollectorKindSafetySnapshotRetention GarbageCollectorKind = "safety-snapshot-retention"
)

// GarbageAction is what a gc that deletes does to one object.
type GarbageAction string

const (
	// GarbageActionDeleteSavepoint deletes a committed savepoint, its record first, then its tree.
	GarbageActionDeleteSavepoint GarbageAction = "delete-savepoint"
)

// GarbageReason is the stable code of why an object is dead, or why it was held back.
type GarbageReason string

const (
	// GarbageReasonBeyondRetention is a safety snapshot older than the newest the config retains.
	GarbageReasonBeyondRetention GarbageReason = "beyond-retention"
)

// GarbageCollectRequest is one gc run.
type GarbageCollectRequest struct {
	DryRun bool
	// DataKinds narrow what the gc looks at; empty looks at every one.
	DataKinds []GarbageDataKind
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

// WithBytes is the summary with bytes in place of its own.
func (m GarbageSummary) WithBytes(bytes *int64) GarbageSummary {
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

// SafetySnapshotStat is a compose project's committed safety snapshots beyond the retention.
type SafetySnapshotStat struct {
	GarbageFindings
	Retained int `json:"retained"`
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

// IsInterrupted is whether the run's stop cut an op short: left it not run, or failed it midway.
func (m GarbageDeletion) IsInterrupted() bool {
	return m.NotRun > 0 || slices.ContainsFunc(m.Failed, func(failure GarbageDeleteFailure) bool {
		return failure.Interrupted
	})
}

// FreedBytes sums the deleted ops' sizes and what was reclaimed; nil when any is unknown.
func (m GarbageDeletion) FreedBytes() *int64 {
	known, unknown := m.KnownFreedBytes()
	if unknown > 0 {
		return nil
	}
	return &known
}

// KnownFreedBytes sums the deleted ops' and the reclaimed sizes that are known, and counts the
// ones that are not.
func (m GarbageDeletion) KnownFreedBytes() (known int64, unknown int) {
	for _, deleted := range m.Deleted {
		if deleted.Bytes == nil {
			unknown++
			continue
		}
		known += *deleted.Bytes
	}
	for _, reclaimed := range m.Reclaimed {
		if reclaimed.Bytes == nil {
			unknown++
			continue
		}
		known += *reclaimed.Bytes
	}
	return known, unknown
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
	// Interrupted is a deletion the run's stop cut short: it may be partly done.
	Interrupted bool `json:"interrupted,omitempty"`
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
}

// IsPartial reports whether anything was left unjudged, aborted, failed or not deleted.
func (m GarbageCollectResult) IsPartial() bool {
	total := m.Total
	return total.Failed > 0 || total.Aborted > 0 || total.Unjudged > 0 || total.DeleteFailed > 0 || total.NotRun > 0
}
