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

// CaptureLabelPrefix opens every label arcave reads as policy. io.arcave.* is not one: those
// are arcave's own, SystemLabel.
const CaptureLabelPrefix = "arcave."

// CaptureLabel is a label a compose project sets its capture policy with.
type CaptureLabel string

const (
	// CaptureLabelCapture is "true" or "false": on a service, everything of it; on a named or
	// anonymous volume, that volume.
	CaptureLabelCapture CaptureLabel = "arcave.capture"
	// CaptureLabelBackend is a volume's backend token, on a named or anonymous volume.
	CaptureLabelBackend CaptureLabel = "arcave.backend"
	// CaptureLabelVolumeBackend is a service's default backend token for its binds and
	// anonymous volumes.
	CaptureLabelVolumeBackend CaptureLabel = "arcave.volume.backend"
	// CaptureLabelMountsExclude lists, comma-separated, the mount targets of a service no
	// savepoint captures; a named volume's target leaves the volume out.
	CaptureLabelMountsExclude CaptureLabel = "arcave.mounts.exclude"
	// CaptureLabelMountsInclude lists the mount targets of a service a savepoint captures
	// whatever the config says of their kind.
	CaptureLabelMountsInclude CaptureLabel = "arcave.mounts.include"
	// CaptureLabelMountsFullCopy and CaptureLabelMountsCDC list the mount targets captured by
	// that backend.
	CaptureLabelMountsFullCopy CaptureLabel = "arcave.mounts.fullcopy"
	CaptureLabelMountsCDC      CaptureLabel = "arcave.mounts.cdc"
	// CaptureLabelWritableLayerCapture is "true" or "false": a service's containers' writable
	// layers.
	CaptureLabelWritableLayerCapture CaptureLabel = "arcave.writable-layer.capture"
	// CaptureLabelWritableLayerBackend is the layout a service's writable layers are stored in.
	CaptureLabelWritableLayerBackend CaptureLabel = "arcave.writable-layer.backend"
	// CaptureLabelImageCapture is "true" or "false": the bytes of the image a service's image:
	// names.
	CaptureLabelImageCapture CaptureLabel = "arcave.image.capture"
)

func (m CaptureLabel) String() string { return string(m) }
