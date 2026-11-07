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

// GroupFilesBySavepoint lets a collector measure a savepoint off the scope's one listing, asking the
// store nothing more. It takes every stored file of a compose project and returns each savepoint's
// own by its id; a file in no savepoint's directory, under shared/ say, is left out.
func GroupFilesBySavepoint(files []storages.ObjectStat) map[model.SavepointID][]storages.ObjectStat {
	filesBySavepoint := map[model.SavepointID][]storages.ObjectStat{}
	for _, file := range files {
		dir, _, _ := strings.Cut(file.Name, "/")
		id := model.SavepointID(dir)
		if id.Validate() != nil {
			continue
		}
		filesBySavepoint[id] = append(filesBySavepoint[id], file)
	}
	return filesBySavepoint
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
