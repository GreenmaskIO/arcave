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

import "strings"

// LayerImageNamespace opens every tag a rollback puts on a restored layer.
const LayerImageNamespace = "arcave"

// LayerTagPrefix opens the tag half of a restored layer's reference; the savepoint id follows.
const LayerTagPrefix = "sp-"

// ImageID is the id of the image the container runs on; empty when the daemon
// reported none or one that is no digest.
func (m Container) ImageID() ImageDigestID {
	if m.Inspect.ContainerJSONBase == nil {
		return ""
	}
	id, err := ParseImageDigestID(m.Inspect.Image)
	if err != nil {
		return ""
	}
	return id
}

// IsLayerRef reports whether ref is a tag a rollback put on a restored layer:
// arcave/…:sp-<savepoint id>.
func IsLayerRef(ref string) bool {
	if !strings.HasPrefix(ref, LayerImageNamespace+"/") {
		return false
	}
	idx := strings.LastIndex(ref, ":")
	if idx < 0 {
		return false
	}
	id, ok := strings.CutPrefix(ref[idx+1:], LayerTagPrefix)
	return ok && SavepointID(id).Validate() == nil
}

// ResolveUpstream is the image c's compose file named: its own image, or its layer's labels
// on an arcave tag. ok is false for a layer carrying no labels or a container with no config.
func ResolveUpstream(res IntrospectionResult, c Container) (id ImageDigestID, ref string, ok bool) {
	if c.Inspect.Config == nil {
		return "", "", false
	}
	name := c.Inspect.Config.Image
	if !IsLayerRef(name) {
		return c.ImageID(), name, true
	}
	return labelledUpstream(res, c.ImageID())
}

// labelledUpstream is the upstream a committed layer's labels name. Only the id
// label is needed: the image it names may be gone from the daemon.
func labelledUpstream(res IntrospectionResult, layerID ImageDigestID) (ImageDigestID, string, bool) {
	inspect, ok := res.Images[layerID]
	if !ok || inspect.Config == nil {
		return "", "", false
	}
	labels := inspect.Config.Labels
	id, err := ParseImageDigestID(labels[BaseImageIDLabel])
	if err != nil {
		return "", "", false
	}
	return id, labels[BaseImageRefLabel], true
}
