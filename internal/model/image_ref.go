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
	"github.com/distribution/reference"
)

// ImageRef is an image reference as the daemon reports it.
type ImageRef string

// Familiar is m as Docker spells it, with no docker.io/ or library/ Podman adds;
// a ref that does not parse stays as it is.
func (m ImageRef) Familiar() ImageRef {
	named, err := reference.ParseNormalizedNamed(string(m))
	if err != nil {
		return m
	}
	return ImageRef(reference.FamiliarString(named))
}
