package core

import (
	"context"
	"fmt"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/greenmaskio/arcave/internal/model"
)

// loadSavepointSpec reads the stack a savepoint was taken of. Every failure here
// happens before anything is stopped.
func (s *Service) loadSavepointSpec(ctx context.Context, record model.Savepoint) (*types.Project, error) {
	if record.Spec == nil {
		return nil, fmt.Errorf("%w: %s", model.ErrSavepointNoSpec, record.ID)
	}
	if record.Spec.Format != model.SpecFormatComposeYAML {
		return nil, fmt.Errorf("savepoint %s carries a compose spec in unknown format %q", record.ID, record.Spec.Format)
	}

	spec, err := s.savepointRepo.LoadSpec(ctx, record.ID)
	if err != nil {
		return nil, err
	}

	project, err := s.projectAssembler.Parse(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("parsing the compose spec of savepoint %s: %w", record.ID, err)
	}
	return project, nil
}
