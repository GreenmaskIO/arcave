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
	"strings"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
)

// NewObjectTrees groups a compose project's listing by the savepoint tree each object lies in.
func NewObjectTrees(objects []storages.ObjectStat) map[model.SavepointID][]storages.ObjectStat {
	trees := map[model.SavepointID][]storages.ObjectStat{}
	for _, object := range objects {
		dir, _, _ := strings.Cut(object.Name, "/")
		trees[model.SavepointID(dir)] = append(trees[model.SavepointID(dir)], object)
	}
	return trees
}

// NewLiveImages maps every image the savepoints hold the bytes of to the first savepoint naming it.
func NewLiveImages(savepoints []model.Savepoint) map[model.ImageDigestID]model.SavepointID {
	live := map[model.ImageDigestID]model.SavepointID{}
	for _, savepoint := range savepoints {
		for _, image := range savepoint.Images {
			if _, seen := live[image.ID]; image.Stored && !seen {
				live[image.ID] = savepoint.ID
			}
		}
	}
	return live
}
