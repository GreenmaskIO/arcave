package model

import "errors"

// What naming a repository can fail with. They live beside Repository so the
// assembly and the doubles can name them too.
var (
	// ErrRepositoryNotFound is every operation's own 404.
	ErrRepositoryNotFound = errors.New("no repository registered under this name")
	// ErrRepositoryNameRegistered reports a name already serving a repository:
	// registration refuses rather than overwrites.
	ErrRepositoryNameRegistered = errors.New("repository name already registered")
)

// RepositoryResolver is how an operation reaches the repository its path names.
// No context: registration is process-local, the server being this process.
type RepositoryResolver interface {
	Lookup(name string) (Repository, error)
}

// RepositoryPublisher is the writing half: which names are served, and for how
// long. Nobody who publishes also resolves.
type RepositoryPublisher interface {
	Register(name string, repo Repository) error
	Unregister(name string)
}
