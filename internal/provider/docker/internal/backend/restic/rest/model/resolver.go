package model

import "errors"

// ErrRepositoryNotFound is every operation's own 404: a first path segment this
// server cannot serve a repository for — empty, carrying a "/", or refused by
// the store's guard for leading outside the root. It lives beside Repository so
// the assembly and the doubles can name it too.
var ErrRepositoryNotFound = errors.New("no repository can be served under this name")

// RepositoryResolver is how an operation reaches the repository its path names.
// No context: resolving is re-rooting a store this process already holds, and
// reaches no backend.
type RepositoryResolver interface {
	Lookup(arcaveObjectID string) (Repository, error)
}
