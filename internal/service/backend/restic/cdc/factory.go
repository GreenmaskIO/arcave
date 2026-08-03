// Package cdc captures a volume into a restic repository that outlives any one
// snapshot, writing only the chunks that changed.
//
// Every restic command runs in a one-shot container of the stock image and
// reaches its repository over the restic server. State questions ("is it
// initialised", "drop this snapshot") skip the container: arcave owns the store
// the repository lives in and answers them server-side (repo_admin.go).
package cdc

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
	resticmodel "github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
	resticrepo "github.com/greenmaskio/arcave/internal/service/backend/restic/rest/repo"
)

// repoNamespace is the segment every volume's repository lives under, inside the
// shared store.
const repoNamespace = "restic"

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

// ContainerRunner is what this backend needs from the Docker engine: it runs
// restic's own containers, so all it wants of a volume is what to bind and, for
// a wipe-mode restore, a way to clear it. *docker.Engine satisfies it.
type ContainerRunner interface {
	RunOneShot(ctx context.Context, spec model.OneShotSpec) error
	ResolveVolumeAddress(ctx context.Context, v model.Volume) (model.VolumeAddress, error)
	WipeVolume(ctx context.Context, addr model.VolumeAddress) error
}

// repositoryPublisher serves one repository under a name for as long as an operation
// needs it. *registry.Registry satisfies it.
type repositoryPublisher interface {
	Register(name string, repo resticmodel.Repository) error
	Unregister(name string)
}

// Options is the backends.cdc config section, translated at the DI root.
type Options struct {
	// Image is the restic image every command runs in.
	Image string
	// Incremental restores over the live data instead of wiping the mount first.
	Incremental bool
	// Overwrite is restic's --overwrite argument, honoured only when Incremental.
	Overwrite string
}

// Dependencies is everything the factory is built over. A struct rather than a parameter
// list: the seams have no natural order, so a new one cannot silently swap with
// an existing one at a call site.
type Dependencies struct {
	Runner    ContainerRunner
	Publisher repositoryPublisher
	// RepoURLBase is the restic repository URL minus its path; a caller appends
	// "<prefix>/" to address one repository. A value and not a seam onto the
	// server: everything in it is known before the first backend is built, so
	// nothing here has to hold a listener to ask it a question.
	RepoURLBase string
	SharedStore storages.Storager
	Options     Options
}

// Factory builds cdc snapshotters over one repository URL base and one shared
// store.
type Factory struct {
	deps Dependencies
}

// NewFactory builds the factory over collaborators it does not own: the server
// the URL base names is already listening, and is closed by whoever started it.
func NewFactory(deps Dependencies) *Factory { return &Factory{deps: deps} }

func (*Factory) Kind() model.SnapshotBackendKind { return model.SnapshotBackendCDC }

// New resolves what is fixed for an instance's life: the volume's repository
// and the publish prefix. The volume's own address is not — resolving it may
// reach the daemon, and a constructor has no ctx to do that on.
func (f *Factory) New(spec model.BackendSpec) (model.VolumeSnapshotBackend, error) {
	// Both tiers address the same repository: restic picks a parent snapshot by
	// host and paths, so a safety backup is an increment over that volume's
	// savepoints. Only the tier label tells the two apart.
	repoStore, err := f.deps.SharedStore.SubStorage(path.Join(repoNamespace, spec.Volume.ObjectID), true)
	if err != nil {
		return nil, fmt.Errorf("re-root store at the %s repository: %w", spec.Volume.ObjectID, err)
	}
	return &Snapshotter{
		volume: spec.Volume,
		// The two halves of the same repository: what a restic container reaches
		// over the protocol, and what arcave addresses as objects of its own.
		repository: resticrepo.New(repoStore),
		admin:      repoAdmin{store: repoStore},
		prefix:     repositoryPrefix(spec.Tier, spec.Volume.ObjectID),
		deps:       f.deps,
		mounted:    path.Join(mountRoot, spec.Volume.ObjectID),
	}, nil
}

// repositoryPrefix names a repository on the registry: one path segment, no "/". The
// tier is in it so a rollback's two instances of one volume collide loudly
// instead of one quietly capturing the other's traffic.
func repositoryPrefix(tier model.BackendTier, objectID string) string {
	return string(tier) + "-" + strings.ReplaceAll(objectID, "/", "-")
}
