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

import "context"

// VolumeSnapshotBackend is bound to one volume and one root, never per call: one
// instance serves a whole workflow, and a rollback reverts through the very
// instances that took its safety snapshot.
type VolumeSnapshotBackend interface {
	// Volume is what the caller attributes its logs and errors by.
	Volume() Volume

	Snapshot(ctx context.Context, req BackendSnapshotRequest) (SnapshotStat, error)
	Restore(ctx context.Context, req BackendRestoreRequest) error
	Remove(ctx context.Context, req BackendRemoveRequest) error
}

// SnapshotBackendFactory builds a backend bound to one spec, for the Kind it
// names. Whatever a kind shares between its instances is built and owned at the
// DI root and lives for the process — a factory has no lifecycle of its own.
type SnapshotBackendFactory interface {
	Kind() SnapshotBackendKind
	New(spec BackendSpec) (VolumeSnapshotBackend, error)
}

// SnapshotBackendRegistry resolves a volume's Backend to its factory and builds
// the instance. Services hold this seam; core ships the implementation.
type SnapshotBackendRegistry interface {
	New(spec BackendSpec) (VolumeSnapshotBackend, error)
}
