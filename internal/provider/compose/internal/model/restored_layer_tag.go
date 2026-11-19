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

package model

import (
	"strings"

	"github.com/greenmaskio/arcave/internal/model"
)

// RestoredLayerTagNamespace opens every tag a rollback puts on a restored layer.
const RestoredLayerTagNamespace = "arcave"

// RestoredLayerTagPrefix opens the tag half of a restored layer's tag; the savepoint id follows.
const RestoredLayerTagPrefix = "sp-"

// IsRestoredLayerTag reports whether ref is the tag a rollback put on a restored layer:
// arcave/…:sp-<savepoint id>.
func IsRestoredLayerTag(ref model.ImageRef) bool {
	_, ok := ParseRestoredLayerTag(ref)
	return ok
}

// ParseRestoredLayerTag is the savepoint a restored layer's tag was put on for; ok is false
// for any other ref.
func ParseRestoredLayerTag(ref model.ImageRef) (model.SavepointID, bool) {
	familiar := string(ref.Familiar())
	if !strings.HasPrefix(familiar, RestoredLayerTagNamespace+"/") {
		return "", false
	}
	idx := strings.LastIndex(familiar, ":")
	if idx < 0 {
		return "", false
	}
	id, ok := strings.CutPrefix(familiar[idx+1:], RestoredLayerTagPrefix)
	if !ok || model.SavepointID(id).Validate() != nil {
		return "", false
	}
	return model.SavepointID(id), true
}
