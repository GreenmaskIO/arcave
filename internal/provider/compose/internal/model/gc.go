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
	"github.com/greenmaskio/arcave/internal/model"
)

// TaggedImage is one tag the daemon holds, with the image it names and that image's origin;
// Origin is zero for an image whose labels name none.
type TaggedImage struct {
	Ref     model.ImageRef
	ImageID model.ImageDigestID
	Origin  LayerOrigin
}

// ContainerImage is what one container, running or not, was created from.
type ContainerImage struct {
	ContainerID string
	// Ref is the reference the container was created with.
	Ref     model.ImageRef
	ImageID model.ImageDigestID
}
