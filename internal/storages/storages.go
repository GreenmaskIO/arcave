// Package storages turns a config.Storage union into the model.Storager the
// backends write through. It is the one place a configured path becomes a store
// root, which makes it the one place that path can be validated.
//
// Nothing above it ever names a directory again: New returns a Storager already
// rooted at the store, so SubStorage is never called with an absolute path and
// the filepath.IsAbs check the DI root used to carry — a check no object store
// could honour — has nowhere left to live.
package storages

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/greenmaskio/gm-tools/internal/config"
	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/storages/directory"
)

// systemRoots are directories a savepoint store must never be. They are the ones
// whose destruction is unrecoverable, and none of them is a plausible store: a
// user who means "/var/lib/gm" writes that, not "/var".
var systemRoots = []string{"/", "/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/opt", "/proc", "/root", "/sbin", "/sys", "/usr", "/var"}

// New builds the Storager for cfg, rooted at the store the config names.
//
// For the directory arm: a relative path is resolved against projectRoot (an
// absolute one is honoured verbatim), the result is validated, then created. The
// MkdirAll is why the factory must exist at all: directory.NewStorage stats its
// path and fails on a missing one, while SubStorage — which the DI root used
// instead — never stats and so silently deferred the failure to the first
// PutObject. Reconciling the two is this function's job.
func New(cfg config.Storage, projectRoot string) (model.Storager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	switch cfg.Type {
	case config.StorageTypeDirectory:
		root := cfg.Directory.Path
		if !filepath.IsAbs(root) {
			root = filepath.Join(projectRoot, root)
		}
		root = filepath.Clean(root)

		if err := validateStoreRoot(root, projectRoot); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, fmt.Errorf("create storage directory %s: %w", root, err)
		}
		return directory.NewStorage(&directory.Config{Path: root})
	default:
		// Unreachable: cfg.Validate above rejects every other tag. The arm exists
		// so adding a storage type without wiring it here fails to compile clean.
		return nil, fmt.Errorf("unsupported storage.type %q", cfg.Type)
	}
}

// validateStoreRoot rejects a store root whose destruction would be
// catastrophic: the filesystem root, a shallow system directory, the user's home
// directory, or an ancestor of the project (which includes the project root
// itself). GM deletes savepoint namespaces inside the store it is given, so the
// store must be a place that exists to hold savepoints and nothing else.
//
// This is defense in depth. directory.Storage.resolve already refuses to delete
// a store's own root, so a bad root cannot cost the whole tree; what this stops
// is a `gm savepoint remove` quietly deleting /etc/1780335122.
func validateStoreRoot(root, projectRoot string) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve storage directory %s: %w", root, err)
	}
	abs = filepath.Clean(abs)

	if slices.Contains(systemRoots, abs) {
		return fmt.Errorf("refusing to use %s as the savepoint store: it is a system directory", abs)
	}
	if home, err := os.UserHomeDir(); err == nil && abs == filepath.Clean(home) {
		return fmt.Errorf("refusing to use %s as the savepoint store: it is your home directory", abs)
	}

	projectAbs, err := filepath.Abs(projectRoot)
	if err != nil {
		return fmt.Errorf("resolve project root %s: %w", projectRoot, err)
	}
	if isAncestor(abs, filepath.Clean(projectAbs)) {
		return fmt.Errorf("refusing to use %s as the savepoint store: it contains the project", abs)
	}
	return nil
}

// isAncestor reports whether dir contains descendant, or is descendant itself.
// It compares cleaned absolute paths rather than string prefixes, so "/a/bc" is
// not treated as living under "/a/b".
func isAncestor(dir, descendant string) bool {
	rel, err := filepath.Rel(dir, descendant)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
