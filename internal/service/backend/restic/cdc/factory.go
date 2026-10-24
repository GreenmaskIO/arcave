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

// Package cdc captures a volume into a restic repository that outlives any one
// snapshot, writing only the chunks that changed.
//
// Every restic command runs in a one-shot container of the stock image and
// reaches its repository over the restic server. State questions ("is it
// initialised", "drop this snapshot") skip the container: arcave owns the store
// the repository lives in and answers them server-side (object_repo.go).
package cdc

import (
	"context"
	"fmt"
	"path"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
)

// NamespaceRepos is the segment every volume's repository lives under, inside the
// shared store.
const NamespaceRepos = "restic"

// resticPassword opens every repository arcave creates. A constant, not a
// secret: whoever can read the store can read .arcave too, and a lost password
// would be lost data. A plain restic CLI opens the repository with it.
const resticPassword = "arcave"

// mountRoot is where the volume is mounted inside the restic container. The
// object id nests under it, so a restore with --target / lands exactly in the
// mounted volume.
//
// It is the path inside every snapshot, and therefore what restic picks a parent
// by: changing it would cost every existing repository its increment.
const mountRoot = "/m"

// VolumeRunner is what this backend needs from the Docker engine: it runs restic
// in throwaway containers over one volume, so it needs what to mount, a way to
// run the container, and — for a wipe-mode restore — a way to clear the volume
// first. *docker.Engine satisfies it.
//
// runner.go uses only the embedded half, which is why that half is an interface
// of its own.
type VolumeRunner interface {
	ContainerRunner
	ResolveMountSource(ctx context.Context, v model.Volume) (model.MountSource, error)
	WipeVolume(ctx context.Context, src model.MountSource) error
}

// Options is the backends.cdc config section, translated at the DI root.
type Options struct {
	// Image is the restic image every command runs in.
	Image string
	// Incremental restores over the live data instead of wiping the mount first.
	Incremental bool
	// Overwrite is restic's --overwrite argument, honoured only when Incremental.
	Overwrite string
	// HostNetwork runs every restic container in the host's network namespace —
	// resolved at the DI root from backends.cdc.network_mode, never probed here.
	HostNetwork bool
}

// Dependencies is everything the factory is built over. A struct rather than a parameter
// list: the seams have no natural order, so a new one cannot silently swap with
// an existing one at a call site.
type Dependencies struct {
	Runner VolumeRunner
	// RepoURLBase is the restic repository URL minus its path; a caller appends
	// "<object-id>/" to address one repository. A value and not a seam onto the
	// server: everything in it is known before the first backend is built, so
	// nothing here has to hold a listener to ask it a question.
	RepoURLBase string
	// RepositoryRoot is the tree the repositories live in — one directory per
	// volume, named by its ObjectID. It is the very root the server resolves
	// against, so a name on the wire and a directory here are the same thing.
	RepositoryRoot storages.Navigator
	Options        Options
}

// Factory builds cdc snapshotters over one repository URL base and one
// repository root.
type Factory struct {
	deps Dependencies
}

// NewFactory builds the factory over collaborators it does not own: the server
// the URL base names is already listening, and is closed by whoever started it.
func NewFactory(deps Dependencies) *Factory { return &Factory{deps: deps} }

func (*Factory) Kind() model.SnapshotBackendKind { return model.SnapshotBackendCDC }

// New resolves what is fixed for an instance's life: the volume's repository.
// The volume's own mount source is not — resolving it may reach the daemon, and a
// constructor has no ctx to do that on.
func (s *Factory) New(spec model.BackendSpec) (model.VolumeSnapshotBackend, error) {
	repoStore, err := s.deps.RepositoryRoot.SubStorage(spec.Volume.ObjectID, true)
	if err != nil {
		return nil, fmt.Errorf("re-root store at the %s repository: %w", spec.Volume.ObjectID, err)
	}
	return &Snapshotter{
		volume: spec.Volume,
		// The two halves of the same repository: what a restic container reaches
		// over the protocol, and what arcave addresses as objects of its own.
		repo:      objectRepo{store: repoStore},
		deps:      s.deps,
		mountPath: path.Join(mountRoot, spec.Volume.ObjectID),
	}, nil
}
