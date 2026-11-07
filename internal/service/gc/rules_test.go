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

package gc

import (
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
)

// Files group by their savepoint's directory; what lies in no savepoint's directory is left out.
func TestGroupFilesBySavepoint(t *testing.T) {
	// Arrange
	const (
		first  = model.SavepointID("019e843e-1650-7000-8000-000000000000")
		second = model.SavepointID("019e844b-7818-7000-8000-000000000000")
	)
	record := storages.ObjectStat{Name: first.String() + "/savepoint.json"}
	archive := storages.ObjectStat{Name: first.String() + "/volume-x/data.tar"}
	other := storages.ObjectStat{Name: second.String() + "/savepoint.json"}
	blob := storages.ObjectStat{Name: "shared/oci/blobs/sha256/abc"}
	loose := storages.ObjectStat{Name: "stray.txt"}

	// Act
	got := GroupFilesBySavepoint([]storages.ObjectStat{record, blob, archive, loose, other})

	// Assert
	assert.Equal(t, map[model.SavepointID][]storages.ObjectStat{
		first:  {record, archive},
		second: {other},
	}, got)
}
