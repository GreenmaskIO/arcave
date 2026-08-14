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
