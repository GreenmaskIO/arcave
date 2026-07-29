// Package cdc captures a volume by content-defined chunking, into a repository
// that outlives any one snapshot: a capture is a tagged snapshot inside it, and
// only changed chunks are written.
//
// Still a stub — it writes nothing, since the repository is not a spec field
// yet; every operation only reports what it was asked to do.
package cdc

import (
	"context"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
)

// Payload is what a capture reports in its SnapshotStat. The real one carries
// the restic snapshot id a restore resolves.
type Payload struct {
	ObjectID   string
	SnapshotID model.SavepointID
	Root       string
}

// Factory builds cdc snapshotters.
type Factory struct{}

// NewFactory builds the factory. The repository handle and the restic tunnel become
// parameters here.
func NewFactory() *Factory { return &Factory{} }

func (Factory) Kind() model.SnapshotBackendKind { return model.SnapshotBackendCDC }

// New narrows nothing: the repository lives outside both trees, so the tier
// decides a write policy here, not a root.
func (Factory) New(spec model.BackendSpec) (model.VolumeSnapshotBackend, error) {
	return Snapshotter{spec: spec}, nil
}

// Snapshotter captures one volume. Existence answers true: a capture this stub
// never wrote is one it cannot find missing either.
type Snapshotter struct {
	spec model.BackendSpec
}

func (s Snapshotter) Volume() model.Volume { return s.spec.Volume }

func (s Snapshotter) Snapshot(_ context.Context, req model.BackendSnapshotRequest) (model.SnapshotStat, error) {
	return model.SnapshotStat{
		Volume: s.spec.Volume,
		Type:   model.SnapshotBackendCDC,
		Payload: Payload{
			ObjectID:   s.spec.Volume.ObjectID,
			SnapshotID: req.SnapshotID,
			Root:       s.root().GetCwd(),
		},
	}, nil
}

func (s Snapshotter) Restore(context.Context, model.BackendRestoreRequest) error { return nil }

func (s Snapshotter) Exists(context.Context, model.BackendExistsRequest) (bool, error) {
	return true, nil
}

func (s Snapshotter) Remove(context.Context, model.BackendRemoveRequest) error { return nil }

// root is what the stub reports in place of a repository, so a test can tell the
// two tiers apart.
func (s Snapshotter) root() storages.Storager {
	if s.spec.Tier == model.BackendTierSafety {
		return s.spec.SafetyStore
	}
	return s.spec.SessionStore
}
