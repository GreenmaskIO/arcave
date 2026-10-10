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
// Root opens <path>/arcave; ArcaveProject, Session and ComposeProject each descend
// one gated segment, and New chains the four.
package storefactory

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"slices"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/rs/zerolog/log"

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

// New opens the store at <path>/arcave/projects/<project>/sessions/<session>/compose/<compose-project>,
// validating every segment, outermost first, before anything is opened.
func New(
	cfg config.Storage,
	projectRoot string,
	arcaveProject model.ArcaveProjectName,
	session model.SessionID,
	composeProject model.DockerComposeProjectName,
) (storages.Storager, error) {
	if err := arcaveProject.Validate(); err != nil {
		return nil, fmt.Errorf("validate arcave project name: %w", err)
	}
	if err := session.Validate(); err != nil {
		return nil, fmt.Errorf("validate session id: %w", err)
	}
	if err := validateComposeProjectName(composeProject); err != nil {
		return nil, fmt.Errorf("validate compose project name: %w", err)
	}

	root, err := Root(cfg, projectRoot)
	if err != nil {
		return nil, err
	}
	arcaveProjectStore, err := ArcaveProject(root, arcaveProject)
	if err != nil {
		return nil, err
	}
	sessionStore, err := Session(arcaveProjectStore, session)
	if err != nil {
		return nil, err
	}
	return ComposeProject(sessionStore, composeProject)
}

// Root opens the store at <path>/arcave. A relative path resolves against projectRoot
// and is created on first use; an absolute one must already exist, so a typo fails.
func Root(cfg config.Storage, projectRoot string) (storages.Storager, error) {
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

// ArcaveProject descends from the root store to projects/<name>.
func ArcaveProject(root storages.Storager, name model.ArcaveProjectName) (storages.Storager, error) {
	if err := name.Validate(); err != nil {
		return nil, fmt.Errorf("validate arcave project name: %w", err)
	}
	return joinKind(root, projectsNamespace, name.String())
}

// Session descends from an arcave project store to sessions/<id>.
func Session(arcaveProjectStore storages.Storager, id model.SessionID) (storages.Storager, error) {
	if err := id.Validate(); err != nil {
		return nil, fmt.Errorf("validate session id: %w", err)
	}
	return joinKind(arcaveProjectStore, sessionsNamespace, id.String())
}

// ComposeProject descends from a session store to compose/<name>.
func ComposeProject(sessionStore storages.Storager, name model.DockerComposeProjectName) (storages.Storager, error) {
	if err := validateComposeProjectName(name); err != nil {
		return nil, fmt.Errorf("validate compose project name: %w", err)
	}
	return joinKind(sessionStore, composeNamespace, name.String())
}

// ComposeProjects lists the compose projects a session holds, sorted by name. A
// directory whose name the compose gate refuses is not one and is skipped.
func ComposeProjects(ctx context.Context, sessionStore storages.Storager) ([]model.DockerComposeProjectName, error) {
	composeStore, err := sessionStore.SubStorage(composeNamespace, true)
	if err != nil {
		return nil, fmt.Errorf("re-root storage at %s: %w", composeNamespace, err)
	}
	_, dirs, err := composeStore.ListDir(ctx)
	if err != nil {
		return nil, fmt.Errorf("list compose projects: %w", err)
	}

	names := make([]model.DockerComposeProjectName, 0, len(dirs))
	for _, dir := range dirs {
		name := model.DockerComposeProjectName(dir.Dirname())
		if err := validateComposeProjectName(name); err != nil {
			log.Ctx(ctx).Debug().Str("entry", name.String()).Msg("ignoring directory that is not a compose project")
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

// joinKind re-roots store at <kind>/<name>; the caller has validated name.
func joinKind(store storages.Storager, kind, name string) (storages.Storager, error) {
	key := path.Join(kind, name)
	sub, err := store.SubStorage(key, true)
	if err != nil {
		return nil, fmt.Errorf("re-root storage at %s: %w", key, err)
	}
	return sub, nil
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
