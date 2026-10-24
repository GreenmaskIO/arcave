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

package utils

import (
	"time"

	"github.com/greenmaskio/storages"
)

// MeasureObjects sums the size of objects and finds the newest write among them; none, or
// one listed without a time, makes the time zero: an unknown write may be the newest.
func MeasureObjects(objects []storages.ObjectStat) (bytes int64, lastModified time.Time) {
	unknown := false
	for _, object := range objects {
		bytes += object.Size
		unknown = unknown || object.LastModified.IsZero()
		if object.LastModified.After(lastModified) {
			lastModified = object.LastModified
		}
	}
	if unknown {
		return bytes, time.Time{}
	}
	return bytes, lastModified
}
