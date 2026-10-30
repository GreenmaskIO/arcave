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

// Package format renders what the renderers print alike.
package format

import "fmt"

// Size renders a byte count in decimal units, to three significant digits at most.
func Size(size int64) string {
	const unit = 1000
	if size < unit {
		return fmt.Sprintf("%dB", size)
	}
	var (
		suffixes = []string{"KB", "MB", "GB", "TB", "PB"}
		value    = float64(size) / unit
		i        int
	)
	for ; value >= 999.5 && i < len(suffixes)-1; i++ {
		value /= unit
	}
	if value < 9.95 {
		return fmt.Sprintf("%.1f%s", value, suffixes[i])
	}
	return fmt.Sprintf("%.0f%s", value, suffixes[i])
}
