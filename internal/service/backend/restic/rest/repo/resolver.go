package repo

import (
	"fmt"
	"strings"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
)

var _ model.RepositoryResolver = (*Resolver)(nil)

// Resolver serves every repository under one root: a name is the directory it
// lives in, so resolving it is re-rooting the store and nothing else.
//
// There is no registration. A repository's name on the wire is its arcave
// ObjectID, which is one path segment by construction, so the tree the server
// was given is the boundary — plus the store's own guard, which refuses a key
// that would leave the root. Nothing has to be published for an operation and
// withdrawn when it ends.
type Resolver struct {
	root storages.Storager
}

// NewResolver returns the resolver over root — the tree every repository lives
// in, and the whole of what this server can reach.
func NewResolver(root storages.Storager) *Resolver {
	return &Resolver{root: root}
}

// Lookup answers with the repository the arcave ObjectID names. It reaches no
// backend and creates nothing: SubStorage only re-roots, so a name that never
// held a repository resolves and then answers 404 to every read of it.
func (r *Resolver) Lookup(arcaveObjectID string) (model.Repository, error) {
	if arcaveObjectID == "" || strings.Contains(arcaveObjectID, "/") {
		return nil, fmt.Errorf("%w: %q", model.ErrRepositoryNotFound, arcaveObjectID)
	}

	store, err := r.root.SubStorage(arcaveObjectID, true)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %w", model.ErrRepositoryNotFound, arcaveObjectID, err)
	}
	return New(store), nil
}
