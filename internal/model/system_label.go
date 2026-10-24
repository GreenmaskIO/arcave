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

// SystemLabelPrefix opens every label arcave writes itself and never reads as policy.
const SystemLabelPrefix = "io.arcave."

// CaptureStampLabelPrefix opens every capture stamp.
const CaptureStampLabelPrefix = SystemLabelPrefix + "capture."

// SystemLabel is a label arcave writes itself: a capture stamp on a service of a savepoint's
// spec, or a label on an image it committed.
type SystemLabel string

const (
	// SystemLabelWritableLayerStamp is the capture stamp of a service's writable layers.
	SystemLabelWritableLayerStamp SystemLabel = CaptureStampLabelPrefix + "writable-layer"
	// SystemLabelImageStamp is the capture stamp of the image a service's image: names.
	SystemLabelImageStamp SystemLabel = CaptureStampLabelPrefix + "image"
	// SystemLabelBaseImageID names, on an image arcave committed, the id of the image the
	// user's compose file named: after a rollback the container runs on an arcave/…:sp-<id>
	// tag, and this label is what still points past it to the true base.
	SystemLabelBaseImageID SystemLabel = SystemLabelPrefix + "base-image-id"
	// SystemLabelBaseImageRef names, beside SystemLabelBaseImageID, the reference the user's
	// compose file named that image by.
	SystemLabelBaseImageRef SystemLabel = SystemLabelPrefix + "base-image-ref"
	// SystemLabelArcaveProject names, on an image arcave committed, the arcave project it was
	// committed under; SystemLabelComposeProject the compose project. A fork copies a layer
	// within both, so neither names a session.
	SystemLabelArcaveProject  SystemLabel = SystemLabelPrefix + "arcave-project"
	SystemLabelComposeProject SystemLabel = SystemLabelPrefix + "compose-project"
)

// NewMountStampLabel is the capture stamp of the volume or bind a service mounts at target.
func NewMountStampLabel(target string) SystemLabel {
	return SystemLabel(CaptureStampLabelPrefix + "mount." + target)
}

func (m SystemLabel) String() string { return string(m) }
