package stack

import (
	"fmt"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/docker/internal/store"
)

// newStackStore resolves the storage section into the one store a stack lives in.
func (p *Provider) newStackStore(
	projectRoot string,
	project model.ProjectName,
	session model.SessionID,
	stackName model.ResourceName,
) (storages.Storager, error) {
	scope := store.Scope{
		Project: project,
		Session: session,
		Family:  model.ProviderFamilyStack,
		Name:    stackName,
	}

	switch p.settings.Storage.Type {
	case storageTypeDirectory:
		directory, err := store.NewDirectory(p.settings.Storage.Directory.Path, projectRoot, scope)
		if err != nil {
			return nil, fmt.Errorf("initialise stack store: %w", err)
		}
		return directory, nil
	default:
		// Unreachable: Settings.Validate rejects every other tag. The arm exists so
		// adding a storage type without wiring it here fails visibly.
		return nil, fmt.Errorf("unsupported storage.type %q", p.settings.Storage.Type)
	}
}
