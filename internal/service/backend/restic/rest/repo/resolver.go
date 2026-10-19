// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
	root storages.Navigator
}

// NewResolver returns the resolver over root — the tree every repository lives
// in, and the whole of what this server can reach.
func NewResolver(root storages.Navigator) *Resolver {
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
