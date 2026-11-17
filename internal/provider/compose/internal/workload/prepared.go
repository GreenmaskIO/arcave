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
	"errors"

	"github.com/greenmaskio/arcave/internal/provider"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

// errForeignPrepared refuses a prepared savepoint another provider handed out.
var errForeignPrepared = errors.New("the savepoint was prepared by another provider")

// preparedSavepoint is a savepoint ready to build, as this provider hands it to core.
type preparedSavepoint struct {
	composemodel.Target
}

// Summary is what building the prepared savepoint restores, and the volumes it overwrites.
func (p preparedSavepoint) Summary() provider.PreparedSummary {
	return provider.PreparedSummary{
		Volumes:        composemodel.VolumesOf(p.Savepoint),
		WritableLayers: p.Restored,
		Overwritten:    p.Overwritten,
	}
}

// toPreparedSavepoint is prepared as this provider prepared it.
func toPreparedSavepoint(prepared provider.PreparedSavepoint) (preparedSavepoint, error) {
	target, ok := prepared.(preparedSavepoint)
	if !ok {
		return preparedSavepoint{}, errForeignPrepared
	}
	return target, nil
}
