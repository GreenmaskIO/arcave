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

// Package prepare makes a savepoint ready to build before anything of the compose project is
// stopped: its backends, its spec checked against its record, its layers loaded into the daemon.
package prepare

import (
	"context"
	"fmt"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/volumebackend"
)

// specReader reads a savepoint's spec artifact by id.
type specReader interface {
	LoadSpec(ctx context.Context, id model.SavepointID) ([]byte, error)
}

// specParser is compose's loader over a spec artifact.
type specParser interface {
	Parse(ctx context.Context, spec []byte) (*types.Project, error)
}

// layerRestorer loads a writable layer out of the store under a tag of its own.
type layerRestorer interface {
	RestoreWritableLayer(ctx context.Context, req model.WritableLayerRestoreRequest) (model.WritableLayerRestoreResult, error)
}

// imageLookup tells whether the daemon holds an image under a reference.
type imageLookup interface {
	ImageRefExists(ctx context.Context, ref string) (bool, error)
}

// Deps are Preparer's collaborators.
type Deps struct {
	SpecReader      specReader
	SpecParser      specParser
	LayerRestorer   layerRestorer
	ImageLookup     imageLookup
	BackendRegistry model.SnapshotBackendRegistry
	// Store is the compose project's store, every backend's root.
	Store storages.Storager
	// LeftoverVolumes is what preparing over an absent compose project does with a volume that
	// outlived it and the build overwrites; empty is model.LeftoverVolumesOverwrite.
	LeftoverVolumes model.LeftoverVolumesPolicy
	// MaxConcurrentOperations caps the layers loaded at once, an errgroup limit.
	MaxConcurrentOperations int
}

// Preparer makes the savepoints of one compose project ready to build.
type Preparer struct {
	specReader              specReader
	specParser              specParser
	layerRestorer           layerRestorer
	imageLookup             imageLookup
	backendRegistry         model.SnapshotBackendRegistry
	store                   storages.Storager
	leftoverVolumes         model.LeftoverVolumesPolicy
	maxConcurrentOperations int
}

// New builds a Preparer from deps.
func New(deps Deps) *Preparer {
	return &Preparer{
		specReader:              deps.SpecReader,
		specParser:              deps.SpecParser,
		layerRestorer:           deps.LayerRestorer,
		imageLookup:             deps.ImageLookup,
		backendRegistry:         deps.BackendRegistry,
		store:                   deps.Store,
		leftoverVolumes:         deps.LeftoverVolumes,
		maxConcurrentOperations: deps.MaxConcurrentOperations,
	}
}

// Prepare builds the savepoint's backends, loads its spec and layers into the daemon and
// points the spec at them. It writes nothing a rollback restores into.
func (s *Preparer) Prepare(
	ctx context.Context, savepoint model.Savepoint, deployment model.SavepointDeployment, overwritten []string,
) (composemodel.Target, error) {
	// Their own set, not the live one's: a volume may have been added or dropped since.
	backends, err := volumebackend.NewBackends(s.backendRegistry, s.store, composemodel.VolumesOf(savepoint))
	if err != nil {
		return composemodel.Target{}, fmt.Errorf("build backends: %w", err)
	}
	project, err := s.loadSavepointSpec(ctx, savepoint)
	if err != nil {
		return composemodel.Target{}, fmt.Errorf("load the savepoint's compose spec: %w", err)
	}
	if err := checkCaptureStamps(ctx, savepoint, project); err != nil {
		return composemodel.Target{}, err
	}
	// Also the check of the archives themselves: one the daemon will not take fails here.
	restored, err := s.restoreWritableLayers(ctx, savepoint, project.Name)
	if err != nil {
		return composemodel.Target{}, fmt.Errorf("restore the savepoint's writable layers: %w", err)
	}
	if err := s.requireImages(ctx, project, restored); err != nil {
		return composemodel.Target{}, err
	}
	return composemodel.Target{
		Savepoint:   savepoint,
		Project:     retargetImages(project, restored),
		Restored:    restored,
		Backends:    backends,
		Deployment:  deployment,
		Overwritten: overwritten,
	}, nil
}

// PrepareHeld loads the layers of savepoint, one captured of the project plan holds, back into the
// daemon and points that project at them, over the backends that captured it: it reads no spec and
// refuses no image.
func (s *Preparer) PrepareHeld(
	ctx context.Context, savepoint model.Savepoint, plan composemodel.Plan, deployment model.SavepointDeployment,
) (composemodel.Target, error) {
	restored, err := s.restoreWritableLayers(ctx, savepoint, plan.Project.Name)
	if err != nil {
		return composemodel.Target{}, err
	}
	return composemodel.Target{
		Savepoint:  savepoint,
		Project:    retargetImages(plan.Project, restored),
		Restored:   restored,
		Backends:   plan.Volumes,
		Deployment: deployment,
	}, nil
}

// NewOverwritten names the leftover volumes savepoint overwrites; under
// model.LeftoverVolumesRefuse any is model.ErrLeftoverVolumes, unless recovering.
func (s *Preparer) NewOverwritten(
	introspection model.IntrospectionResult, savepoint model.Savepoint, recovering bool,
) ([]string, error) {
	leftover := leftoverVolumes(composemodel.OwnedVolumes(introspection), savepoint)
	if len(leftover) > 0 && s.leftoverVolumes == model.LeftoverVolumesRefuse && !recovering {
		return nil, fmt.Errorf("%w: %s", model.ErrLeftoverVolumes, strings.Join(leftover, ", "))
	}
	return leftover, nil
}

// leftoverVolumes names the volumes that outlived the project's containers and savepoint overwrites.
func leftoverVolumes(owned map[string]bool, savepoint model.Savepoint) []string {
	var names []string
	for _, v := range composemodel.VolumesOf(savepoint) {
		if v.Named != nil && owned[v.Named.VolumeName] {
			names = append(names, v.Named.VolumeName)
		}
	}
	return names
}

// loadSavepointSpec reads the compose project a savepoint was taken of. Every failure here
// happens before anything is stopped.
func (s *Preparer) loadSavepointSpec(ctx context.Context, savepoint model.Savepoint) (*types.Project, error) {
	if savepoint.Spec == nil {
		return nil, fmt.Errorf("%w: %s", model.ErrSavepointNoSpec, savepoint.ID)
	}
	if savepoint.Spec.Format != model.SpecFormatComposeYAML {
		return nil, fmt.Errorf("savepoint %s carries a compose spec in unknown format %q", savepoint.ID, savepoint.Spec.Format)
	}

	spec, err := s.specReader.LoadSpec(ctx, savepoint.ID)
	if err != nil {
		return nil, err
	}

	project, err := s.specParser.Parse(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("parsing the compose spec of savepoint %s: %w", savepoint.ID, err)
	}
	return project, nil
}
