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

package workload

import (
	"context"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/greenmaskio/arcave/internal/provider"
)

// builtWorkload is the compose project created from the prepared savepoint, labelled, and empty.
type builtWorkload struct {
	vacant  vacantWorkload
	target  preparedSavepoint
	project *types.Project
}

// RestoreData writes every volume of the savepoint back.
func (b builtWorkload) RestoreData(ctx context.Context) (provider.RestoredWorkload, error) {
	if err := b.vacant.service.deployer.RestoreVolumes(ctx, b.target.Savepoint, b.target.Backends); err != nil {
		return nil, err
	}
	return restoredWorkload(b), nil
}
