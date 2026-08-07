// Package storefactory turns a config.Storage union into the storages.Storager
// the backends write through. It is the one place a configured path becomes a
// store root, which makes it the one place the store namespace and the segments
// below it are appended.
//
// Nothing above it ever names a directory again: New returns a Storager already
// rooted at the stack, so SubStorage is never called with an absolute path and
// the filepath.IsAbs check the DI root used to carry — a check no object store
// could honour — has nowhere left to live. It stops at the stack: everything
// below is the savepoint layer's and the backends' own layout, and pre-narrowing
// to a savepoint would decide here what only a backend knows.
//
// The backends themselves are greenmaskio/storages' (directory today; s3, azure,
// ssh and inmemory without new work in arcave), and arcave speaks their interface
// directly: every constructor there returns a store whose keys are already gated
// against escaping it, so there is nothing left here to adapt or re-wrap.
package storefactory

import (
	"fmt"
	"path"
	"path/filepath"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
)

// storeNamespace is the static segment every store is rooted under, appended to
// whatever the config names. It is not configurable, and it is the whole safety
// story for destructive operations: arcave deletes inside the store it is given —
// savepoint namespaces today, a future `arcave savepoint gc` sweeping unrecorded
// directories tomorrow — and the namespace guarantees by construction that
// everything under the store root is arcave's own data, wherever the user points
// the path. Point it at /etc and arcave operates in /etc/arcave; dedicate a
// bucket to it and arcave stays inside the arcave/ prefix. The library's Guard
// closes the other direction — no key escapes the root — so the two together
// bound every delete to arcave-owned keys on any backend, which is what replaced
// the old validateStoreRoot blocklist: that one enumerated bad filesystem roots
// and had nothing to say for an object store. Every future arm (s3, …) must
// root its store under this same segment.
const storeNamespace = "arcave"

// The static segments between the namespace and the stack. They name what the
// value after them is, so one configured path can hold every project a user
// points at it and a reader of the tree can tell a tenant from a lifetime
// without knowing arcave's vocabulary.
const (
	projectsNamespace = "projects"
	sessionsNamespace = "sessions"
	composeNamespace  = "compose"
)

// New builds the Storager for cfg, rooted at
// <path>/arcave/projects/<project>/sessions/<session>/compose/<stack>: the
// storage tenant, the lifetime everything under it dies with, and the compose
// stack whose volumes it holds. For the directory arm a relative path is
// resolved against projectRoot; an absolute one is honoured verbatim.
//
// The session is self-contained by construction — a backend's own tree lives
// under the stack like everything else, cdc's restic repositories included — so
// dropping a session drops all of it, at the price of a cdc repository that
// cannot be shared across sessions. compose/ has a reserved sibling: sandbox/,
// the second timeline, which gets a constructor of its own here when it is built.
//
// The store's prefix is created by the backend (WithCreatePrefix), never its
// root: a relative path is rooted at projectRoot — which exists by definition —
// so the whole configured path materializes on first use, while an absolute path
// must already exist, keeping a typo from quietly growing a fresh tree. The
// segments below the namespace still arrive with the first object written under
// them.
func New(
	cfg config.Storage,
	projectRoot string,
	project model.ProjectName,
	session model.SessionID,
	stack model.StackName,
) (storages.Storager, error) {
	// Every segment is rendered into a store root, so each is gated at the layer
	// that owns the value, before any of it reaches a backend. Validated in path
	// order, so a config naming two bad segments is reported outermost-first.
	if err := project.Validate(); err != nil {
		return nil, fmt.Errorf("validate project name: %w", err)
	}
	if err := session.Validate(); err != nil {
		return nil, fmt.Errorf("validate session id: %w", err)
	}
	if err := stack.Validate(); err != nil {
		return nil, fmt.Errorf("validate stack name: %w", err)
	}

	store, err := getStorager(cfg, projectRoot)
	if err != nil {
		return nil, err
	}

	key := path.Join(
		projectsNamespace, project.String(),
		sessionsNamespace, session.String(),
		composeNamespace, stack.String(),
	)
	stackStore, err := store.SubStorage(key, true)
	if err != nil {
		return nil, fmt.Errorf("re-root storage at %s: %w", key, err)
	}
	return stackStore, nil
}

// getStorager resolves the config union into a store rooted at <path>/arcave — one arm
// per storage type, and nothing else: what the segments below mean is New's, so a
// new arm lands here alone.
func getStorager(cfg config.Storage, projectRoot string) (storages.Storager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	switch cfg.Type {
	case config.StorageTypeDirectory:
		rootPath, prefix := projectRoot, path.Join(filepath.ToSlash(cfg.Directory.Path), storeNamespace)
		if filepath.IsAbs(cfg.Directory.Path) {
			rootPath, prefix = cfg.Directory.Path, storeNamespace
		}
		return directory.NewStorage(directory.Config{
			RootPath: rootPath,
			Prefix:   prefix,
		}, directory.WithCreatePrefix())
	default:
		// Unreachable: cfg.Validate above rejects every other tag. The arm exists
		// so adding a storage type without wiring it here fails to compile clean.
		return nil, fmt.Errorf("unsupported storage.type %q", cfg.Type)
	}
}
