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

// ResolveUpstream is the image c's compose file named: its own image, or its layer's labels
// on an arcave tag. ok is false for a layer carrying no labels or a container with no config.
func ResolveUpstream(res IntrospectionResult, c Container) (id ImageDigestID, ref string, ok bool) {
	if c.Inspect.Config == nil {
		return "", "", false
	}
	name := c.Inspect.Config.Image
	if !ImageRef(name).IsRestoredLayerTag() {
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
	id, err := ParseImageDigestID(labels[string(SystemLabelBaseImageID)])
	if err != nil {
		return "", "", false
	}
	return id, labels[string(SystemLabelBaseImageRef)], true
}
