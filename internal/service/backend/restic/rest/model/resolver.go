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
