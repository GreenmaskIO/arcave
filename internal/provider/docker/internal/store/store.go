// Package store turns a storage location into the Storager the backends
// write through, already rooted at the unit a provider drives; below that is
// theirs. It names no provider's config: a provider resolves its own storage
// section and calls the arm it named.
package store

import (
	"fmt"
	"path"
	"path/filepath"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"

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

// The static segments between the namespace and the unit. They name what the
// value after them is, so one configured path can hold every project a user
// points at it and a reader of the tree can tell a tenant from a lifetime
// without knowing arcave's vocabulary.
const (
	projectsNamespace = "projects"
	sessionsNamespace = "sessions"
)

// Scope is the addressing one store root is scoped by: the tenant, the lifetime,
// and the provider's own unit under the family that drives it.
type Scope struct {
	Project model.ProjectName
	Session model.SessionID
	Family  model.ProviderFamily
	Name    model.ResourceName
}

// Validate gates every segment before it reaches a backend, in path order so the
// outermost bad one is reported.
func (s Scope) Validate() error {
	if err := s.Project.Validate(); err != nil {
		return fmt.Errorf("validate project name: %w", err)
	}
	if err := s.Session.Validate(); err != nil {
		return fmt.Errorf("validate session id: %w", err)
	}
	if err := s.Family.Validate(); err != nil {
		return fmt.Errorf("validate provider family: %w", err)
	}
	if err := s.Name.Validate(); err != nil {
		return fmt.Errorf("validate resource name: %w", err)
	}
	return nil
}

// key renders the scope as the path below the namespace.
func (s Scope) key() string {
	return path.Join(
		projectsNamespace, s.Project.String(),
		sessionsNamespace, s.Session.String(),
		s.Family.String(), s.Name.String(),
	)
}

// NewDirectory builds the local-filesystem store for path, rooted at
// <path>/arcave/projects/<project>/sessions/<session>/<family>/<name>.
// A relative path is resolved against projectRoot.
func NewDirectory(dirPath, projectRoot string, scope Scope) (storages.Storager, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if dirPath == "" {
		return nil, fmt.Errorf("storage path is required")
	}

	rootPath, prefix := projectRoot, path.Join(filepath.ToSlash(dirPath), storeNamespace)
	if filepath.IsAbs(dirPath) {
		rootPath, prefix = dirPath, storeNamespace
	}
	store, err := directory.NewStorage(directory.Config{
		RootPath: rootPath,
		Prefix:   prefix,
	}, directory.WithCreatePrefix())
	if err != nil {
		return nil, err
	}
	return rooted(store, scope)
}

// rooted re-roots store at the scope's key — the layout every arm shares.
func rooted(store storages.Storager, scope Scope) (storages.Storager, error) {
	key := scope.key()
	scoped, err := store.SubStorage(key, true)
	if err != nil {
		return nil, fmt.Errorf("re-root storage at %s: %w", key, err)
	}
	return scoped, nil
}
