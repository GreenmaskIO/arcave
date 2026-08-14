// Package model holds the vocabulary of one restic repository: how an object is
// addressed and which failures the protocol has a status for.
package model

import (
	"errors"
	"fmt"
)

// The two conditions the protocol answers with a status of its own, and so the
// whole of what a repository promises beyond "it worked".
var (
	// ErrObjectNotFound is the 404 of a HEAD and a GET; a delete swallows it,
	// since a retry after a first success must not fail a prune.
	ErrObjectNotFound = errors.New("object not found")
	// ErrConfigExists reports a config already written: restic's init depends
	// on the refusal.
	ErrConfigExists = errors.New("config already exists")
)

// The two ways an address can fail to be one. What to answer is each
// operation's call, which today is the same 404 either way.
var (
	// ErrInvalidObjectType reports a namespace this protocol does not have.
	ErrInvalidObjectType = errors.New("invalid object type")
	// ErrInvalidObjectID reports a name that is not a restic object name.
	ErrInvalidObjectID = errors.New("invalid object id")
)

// ObjectType is the namespace an object lives in. Its only values are the ones
// ParseObjectType admits, so an unchecked segment cannot become a storage key.
type ObjectType string

// The namespaces a repository is addressed by. `config` is not among them: it
// has no name, its own path, and is never listed.
const (
	TypeData      ObjectType = "data"
	TypeKeys      ObjectType = "keys"
	TypeLocks     ObjectType = "locks"
	TypeSnapshots ObjectType = "snapshots"
	TypeIndex     ObjectType = "index"
)

// ParseObjectType admits the five namespaces and nothing else: a list of what a
// segment must not be is never provably complete, while a known shape is.
func ParseObjectType(s string) (ObjectType, error) {
	switch t := ObjectType(s); t {
	case TypeData, TypeKeys, TypeLocks, TypeSnapshots, TypeIndex:
		return t, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidObjectType, s)
	}
}

// objectIDLength is the hex width of a 256-bit content hash.
const objectIDLength = 64

// ObjectID is one object's name — 64 lowercase hex characters — and carries the
// same guarantee ObjectType does.
type ObjectID string

// ParseObjectID admits exactly 64 lowercase hex characters. Upper case is
// refused, not folded: it would let one object answer to two keys.
func ParseObjectID(s string) (ObjectID, error) {
	if len(s) != objectIDLength {
		return "", fmt.Errorf("%w: %q is %d characters, want %d", ErrInvalidObjectID, s, len(s), objectIDLength)
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("%w: %q is not lowercase hex", ErrInvalidObjectID, s)
		}
	}
	return ObjectID(s), nil
}

// Blob is one object of a listing: the name restic knows it by and its size.
type Blob struct {
	Name string
	Size int64
}
