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

// Package storefactory turns a config.Storage union into the storages.Storager
// the backends write through. It is the one place a configured path becomes a
// store root, which makes it the one place the store namespace and the segments
// below it are appended.
//
// New returns a Storager already rooted at the compose project; everything below that is
// the savepoint layer's and the backends' own layout.
package storefactory

import (
	"fmt"
	"path"
	"path/filepath"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
)

// storeNamespace is the static segment every arm must root its store under,
// appended to whatever the config names. It is not configurable: it guarantees
// everything under the store root is arcave's own data, bounding every delete
// to it — point the path at /etc and arcave operates in /etc/arcave.
const storeNamespace = "arcave"

// The static segments between the namespace and the compose project. They name what the
// value after them is, so one configured path can hold every project a user
// points at it and a reader of the tree can tell a tenant from a lifetime
// without knowing arcave's vocabulary.
const (
	projectsNamespace = "projects"
	sessionsNamespace = "sessions"
	composeNamespace  = "compose"
)

// New builds the Storager for cfg, rooted at
// <path>/arcave/projects/<project>/sessions/<session>/compose/<compose-project>. For the
// directory arm a relative path is resolved against projectRoot; an absolute one
// is honoured verbatim.
//
// The store's prefix is created by the backend (WithCreatePrefix), never its
// root: a relative path materializes on first use, while an absolute one must
// already exist, keeping a typo from quietly growing a fresh tree.
func New(
	cfg config.Storage,
	projectRoot string,
	arcaveProject model.ArcaveProjectName,
	session model.SessionID,
	composeProject model.DockerComposeProjectName,
) (storages.Storager, error) {
	// Every segment is rendered into a store root, so each is gated at the layer
	// that owns the value, before any of it reaches a backend. Validated in path
	// order, so a config naming two bad segments is reported outermost-first.
	if err := arcaveProject.Validate(); err != nil {
		return nil, fmt.Errorf("validate arcave project name: %w", err)
	}
	if err := session.Validate(); err != nil {
		return nil, fmt.Errorf("validate session id: %w", err)
	}
	if err := validateComposeProjectName(composeProject); err != nil {
		return nil, fmt.Errorf("validate compose project name: %w", err)
	}

	store, err := getStorager(cfg, projectRoot)
	if err != nil {
		return nil, err
	}

	key := path.Join(
		projectsNamespace, arcaveProject.String(),
		sessionsNamespace, session.String(),
		composeNamespace, composeProject.String(),
	)
	composeProjectStore, err := store.SubStorage(key, true)
	if err != nil {
		return nil, fmt.Errorf("re-root storage at %s: %w", key, err)
	}
	return composeProjectStore, nil
}

// validateComposeProjectName accepts a name compose itself would produce — one its own
// normalization leaves unchanged — rendering as a single path segment. The charset is
// compose's to own; the segment rule is the store's.
func validateComposeProjectName(name model.DockerComposeProjectName) error {
	s := name.String()
	if s == "" {
		return fmt.Errorf("invalid compose project name: must not be empty")
	}
	if normalized := loader.NormalizeProjectName(s); normalized != s {
		return fmt.Errorf("invalid compose project name %q: compose would name it %q", s, normalized)
	}
	if path.Base(s) != s || s == "." || s == ".." {
		return fmt.Errorf("invalid compose project name %q: must be a single path segment", s)
	}
	return nil
}

// getStorager resolves the config union into a store rooted at <path>/arcave — one arm
// per storage type, and nothing else: what the segments below mean is New's, so a
// new arm lands here alone.
func getStorager(cfg config.Storage, projectRoot string) (storages.Storager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	switch cfg.Type {
	case config.StorageTypeDirectory:
		rootPath, prefix := projectRoot, path.Join(filepath.ToSlash(cfg.Directory.Path), storeNamespace)
		if filepath.IsAbs(cfg.Directory.Path) {
			rootPath, prefix = cfg.Directory.Path, storeNamespace
		}
		return directory.NewStorage(directory.Config{
			RootPath: rootPath,
			Prefix:   prefix,
		}, directory.WithCreatePrefix())
	default:
		// Unreachable: cfg.Validate above rejects every other tag. The arm exists
		// so adding a storage type without wiring it here fails to compile clean.
		return nil, fmt.Errorf("unsupported storage.type %q", cfg.Type)
	}
}
