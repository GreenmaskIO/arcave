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

// VolumeSnapshotBackend captures and restores one volume, bound to one root; it never deletes.
// Anything it writes outside the savepoint's tree needs a gc collector.
type VolumeSnapshotBackend interface {
	// Volume is what the caller attributes its logs and errors by.
	Volume() Volume

	Snapshot(ctx context.Context, req BackendSnapshotRequest) (SnapshotStat, error)
	Restore(ctx context.Context, req BackendRestoreRequest) error
}

// SnapshotBackendFactory builds a backend bound to one spec, for the Kind it names. What its
// instances share is built at the DI root and lives for the process.
type SnapshotBackendFactory interface {
	Kind() composeconfig.SnapshotBackendKind
	New(spec BackendSpec) (VolumeSnapshotBackend, error)
}

// SnapshotBackendRegistry resolves a volume's Backend to its factory and builds the instance.
type SnapshotBackendRegistry interface {
	New(spec BackendSpec) (VolumeSnapshotBackend, error)
}

// BackendSpec binds a snapshot backend to one volume and the root it may write under; the
// snapshot id is the request's, the kind Volume.Backend's.
type BackendSpec struct {
	// Store is the root the backend lays out under: a savepoint's tree or a rollback's undo tree.
	// A backend whose data lives elsewhere (cdc) may ignore it.
	Store  storages.Navigator
	Volume Volume
}

// One request per kind of backend operation; the root an instance writes to is bound at construction.
// SnapshotID addresses either kind of capture — a savepoint's or a safety snapshot's.
type (
	BackendSnapshotRequest struct {
		SnapshotID model.SavepointID
		// Labels are the savepoint's marks, rendered into the backend's own store where it has a place.
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
