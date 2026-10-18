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

package core

import (
	"context"
	"fmt"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/greenmaskio/arcave/internal/model"
)

// loadSavepointSpec reads the compose project a savepoint was taken of. Every failure here
// happens before anything is stopped.
func (s *Service) loadSavepointSpec(ctx context.Context, savepoint model.Savepoint) (*types.Project, error) {
	if savepoint.Spec == nil {
		return nil, fmt.Errorf("%w: %s", model.ErrSavepointNoSpec, savepoint.ID)
	}
	if savepoint.Spec.Format != model.SpecFormatComposeYAML {
		return nil, fmt.Errorf("savepoint %s carries a compose spec in unknown format %q", savepoint.ID, savepoint.Spec.Format)
	}

	spec, err := s.savepointRepo.LoadSpec(ctx, savepoint.ID)
	if err != nil {
		return nil, err
	}

	project, err := s.specCodec.Parse(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("parsing the compose spec of savepoint %s: %w", savepoint.ID, err)
	}
	return project, nil
}
