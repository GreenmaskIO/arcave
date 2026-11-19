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
	"context"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
)

// VolumeSnapshotBackend captures and restores one volume; it never deletes. What it
// writes under the savepoint's tree goes with the tree, and anything beside it needs
// a gc collector. It is bound to one volume and one root, never per call: one instance
// serves a whole workflow, and a rollback reverts through the very instances that took
// its safety snapshot.
type VolumeSnapshotBackend interface {
	// Volume is what the caller attributes its logs and errors by.
	Volume() Volume

	Snapshot(ctx context.Context, req BackendSnapshotRequest) (SnapshotStat, error)
	Restore(ctx context.Context, req BackendRestoreRequest) error
}

// SnapshotBackendFactory builds a backend bound to one spec, for the Kind it
// names. Whatever a kind shares between its instances is built and owned at the
// DI root and lives for the process — a factory has no lifecycle of its own.
type SnapshotBackendFactory interface {
	Kind() composeconfig.SnapshotBackendKind
	New(spec BackendSpec) (VolumeSnapshotBackend, error)
}

// SnapshotBackendRegistry resolves a volume's Backend to its factory and builds
// the instance. The compose provider holds this seam and ships the implementation.
type SnapshotBackendRegistry interface {
	New(spec BackendSpec) (VolumeSnapshotBackend, error)
}

// BackendSpec binds a snapshot backend to one volume and the root it may write
// under. It carries no snapshot id — that is the request's, so one instance
// serves a whole workflow — and no kind: Volume.Backend is what the registry
// dispatches on.
type BackendSpec struct {
	// Store is the root the caller hands over: a savepoint's tree for a savepoint,
	// a rollback's undo tree for a safety snapshot. Which of them it is, is the
	// caller's call — the backend only knows it is a root, and what it lays out
	// below it is its own policy. A backend whose data lives elsewhere (cdc, whose
	// repository outlives every session) may ignore it.
	Store  storages.Navigator
	Volume Volume
}

// One request per kind of backend operation; the root an instance writes to is bound at construction.
// SnapshotID addresses either kind of capture — a savepoint's or a safety snapshot's.
type (
	BackendSnapshotRequest struct {
		SnapshotID model.SavepointID
		// Labels are the savepoint's marks, for the backend to render into
		// whatever marking its own store understands. A backend with nowhere to
		// put them ignores them.
		Labels map[string]string
	}

	BackendRestoreRequest struct {
		SnapshotID model.SavepointID
		// Payload is the SnapshotStat.Payload the write reported; only the backend
		// that produced it interprets it.
		Payload any
		// SizeBytes is the SnapshotStat.SizeBytes the write reported, 0 when
		// unknown: a total for progress, never a check.
		SizeBytes int64
	}
)
