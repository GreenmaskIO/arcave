// Package registry holds the one implementation of the two seams a repository is
// named through.
package registry

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
)

// errInvalidName reports a name that is not one non-empty path segment.
var errInvalidName = errors.New(`repository name must be one non-empty path segment without "/"`)

// docsName is the documentation's own segment. Echo prefers a static segment to
// a parametric one, so a repository registered under it is reachable by nobody.
const docsName = "docs"

var (
	_ model.RepositoryResolver  = (*Registry)(nil)
	_ model.RepositoryPublisher = (*Registry)(nil)
)

// Registry serves many repositories from one endpoint, each under the first
// segment of a request's path.
type Registry struct {
	mu    sync.RWMutex
	repos map[string]model.Repository
}

// New returns an empty Registry; every name is ErrRepositoryNotFound until Register.
func New() *Registry {
	return &Registry{repos: make(map[string]model.Repository)}
}

// Register serves repo under name — one path segment, flattened by the caller.
// A name already taken is refused: two operations would read each other's data.
func (r *Registry) Register(name string, repo model.Repository) error {
	if name == "" || strings.Contains(name, "/") {
		return fmt.Errorf("%w: %q", errInvalidName, name)
	}
	if name == docsName {
		return fmt.Errorf("%w: %q is the documentation's own segment", errInvalidName, name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, taken := r.repos[name]; taken {
		return fmt.Errorf("%w: %s", model.ErrRepositoryNameRegistered, name)
	}
	r.repos[name] = repo
	return nil
}

// Unregister stops serving name. Nothing can fail here, so a deferred withdrawal
// has no error to reconcile; a request already reading holds its own reference.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.repos, name)
}

// Lookup answers with the repository served under name.
func (r *Registry) Lookup(name string) (model.Repository, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	repo, ok := r.repos[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", model.ErrRepositoryNotFound, name)
	}
	return repo, nil
}
