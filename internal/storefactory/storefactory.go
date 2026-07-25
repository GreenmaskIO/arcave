// Package storefactory turns a config.Storage union into the storages.Storager
// the backends write through. It is the one place a configured path becomes a
// store root, which makes it the one place the store namespace is appended.
//
// Nothing above it ever names a directory again: New returns a Storager already
// rooted at the store, so SubStorage is never called with an absolute path and
// the filepath.IsAbs check the DI root used to carry — a check no object store
// could honour — has nowhere left to live.
//
// The backends themselves are greenmaskio/storages' (directory today; s3, azure,
// ssh and inmemory without new work in arcave), and arcave speaks their interface
// directly: every constructor there returns a store whose keys are already gated
// against escaping it, so there is nothing left here to adapt or re-wrap.
package storefactory

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"

	"github.com/greenmaskio/arcave/internal/config"
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

// New builds the Storager for cfg, rooted at the store the config names plus
// the storeNamespace segment.
//
// For the directory arm: a relative path is resolved against projectRoot (an
// absolute one is honoured verbatim), the namespace is appended, and the result
// is created. The MkdirAll is why the factory must exist at all:
// directory.NewStorage stats its path and fails on a missing one, while
// SubStorage — which the DI root used instead — never stats and so silently
// deferred the failure to the first PutObject. Reconciling the two is this
// function's job.
func New(cfg config.Storage, projectRoot string) (storages.Storager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	switch cfg.Type {
	case config.StorageTypeDirectory:
		root := cfg.Directory.Path
		if !filepath.IsAbs(root) {
			root = filepath.Join(projectRoot, root)
		}
		root = filepath.Join(root, storeNamespace)

		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, fmt.Errorf("create storage directory %s: %w", root, err)
		}
		return directory.NewStorage(directory.Config{Path: root})
	default:
		// Unreachable: cfg.Validate above rejects every other tag. The arm exists
		// so adding a storage type without wiring it here fails to compile clean.
		return nil, fmt.Errorf("unsupported storage.type %q", cfg.Type)
	}
}
