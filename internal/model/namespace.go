package model

import "fmt"

// NamespaceKind distinguishes the two things GM snapshots: the savepoints a user
// creates and names, and the throwaway safety backup a rollback takes of the
// live state before it touches anything. They share a backend and a layout, so
// only the kind tells them apart.
type NamespaceKind string

const (
	NamespaceSavepoint    NamespaceKind = "savepoint"
	NamespaceSafetyBackup NamespaceKind = "safety-backup"
)

// Namespace addresses the whole of one snapshot's data inside a store. A backend
// lays every object it writes for that snapshot under the namespace, and
// Remove(ns) drops it whole. The backend chooses how to render it — an object-key
// prefix for the copy backend, a subvolume name for btrfs — so a namespace is a
// value, never a path.
//
// The store a namespace lives in is not named here: that is the backend's
// Storager, bound at the DI seam.
type Namespace struct {
	Kind NamespaceKind
	ID   string
}

// SavepointNamespace addresses savepoint id's data.
func SavepointNamespace(id string) Namespace {
	return Namespace{
		Kind: NamespaceSavepoint,
		ID:   id,
	}
}

// SafetyBackupNamespace addresses the safety backup a rollback took at id.
func SafetyBackupNamespace(id string) Namespace {
	return Namespace{Kind: NamespaceSafetyBackup, ID: id}
}

// String renders the namespace as the backend addresses it: the bare id for a
// savepoint, "rollback-<id>" for a safety backup. It is the one place the
// "rollback-" prefix is spelled.
func (n Namespace) String() string {
	if n.Kind == NamespaceSafetyBackup {
		return "rollback-" + n.ID
	}
	return n.ID
}

// Validate rejects a namespace a backend must not act on. It is the second of
// the three path-safety gates: the backend calls it before rendering the
// namespace into a storage key, so an id that never was a Unix timestamp cannot
// reach a syscall even if it somehow reached a record.
func (n Namespace) Validate() error {
	switch n.Kind {
	case NamespaceSavepoint, NamespaceSafetyBackup:
	default:
		return fmt.Errorf("unknown namespace kind %q", n.Kind)
	}
	return ValidSavepointID(n.ID)
}

// ValidSavepointID accepts only a non-empty run of digits — savepoint IDs are
// Unix seconds. It is the first of three path-safety gates: a hand-planted
// "../x.json" can never become an ID that reaches a syscall.
func ValidSavepointID(id string) error {
	if id == "" {
		return fmt.Errorf("savepoint id is empty")
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return fmt.Errorf("savepoint id %q is not a unix timestamp", id)
		}
	}
	return nil
}

// VolumeKey addresses one volume within a namespace: the compose service that
// owns the mount and the mount's target path inside the container. Keying on
// service + target (rather than a Docker volume name) is what lets named
// volumes, bind mounts and anonymous volumes share one scheme.
type VolumeKey struct {
	Service   string
	MountPath string // container mount target, e.g. "/export/data"
}
