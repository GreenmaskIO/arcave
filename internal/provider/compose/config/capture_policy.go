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

package config

// ParseVolumeBackend validates a volume backend token, as config and labels spell it.
func ParseVolumeBackend(token string) (SnapshotBackendKind, bool) {
	switch kind := SnapshotBackendKind(token); kind {
	case SnapshotBackendFullCopy, SnapshotBackendCDC:
		return kind, true
	default:
		return SnapshotBackendUnknown, false
	}
}

// ParseImageBackend validates an image backend token.
func ParseImageBackend(token string) (ImageBackendKind, bool) {
	switch kind := ImageBackendKind(token); kind {
	case ImageBackendDedupCopy, ImageBackendFullCopy:
		return kind, true
	default:
		return "", false
	}
}

// CaptureMode is how a service is held still while a savepoint captures it.
type CaptureMode string

const (
	// CaptureModeStop stops the service's containers.
	CaptureModeStop CaptureMode = "stop"
	// CaptureModePause freezes them: the capture is crash-consistent and no connection drops.
	CaptureModePause CaptureMode = "pause"
	// CaptureModeLive holds nothing: the service is captured as it runs.
	CaptureModeLive CaptureMode = "live"
)

// ParseCaptureMode validates a capture mode, as config and labels spell it.
func ParseCaptureMode(token string) (CaptureMode, bool) {
	switch mode := CaptureMode(token); mode {
	case CaptureModeStop, CaptureModePause, CaptureModeLive:
		return mode, true
	default:
		return "", false
	}
}

// SnapshotBackendKind names the mechanism that captures a volume, assigned where a savepoint's
// units are decided, never by introspection.
type SnapshotBackendKind string

// The tokens snapshot.backend accepts, and the kind of a token it does not.
const (
	// SnapshotBackendFullCopy stores each mount's tar stream whole, in the format
	// backends.fullcopy.compression names.
	SnapshotBackendFullCopy SnapshotBackendKind = "fullcopy"
	// SnapshotBackendCDC captures into a restic repository that outlives any one
	// savepoint, writing only the chunks that changed.
	SnapshotBackendCDC     SnapshotBackendKind = "cdc"
	SnapshotBackendUnknown SnapshotBackendKind = "unknown"
)

// HookOnError is what a failed hook does to the capture.
type HookOnError string

const (
	// HookOnErrorFail fails the workflow.
	HookOnErrorFail HookOnError = "fail"
	// HookOnErrorContinue warns and goes on.
	HookOnErrorContinue HookOnError = "continue"
)

// ParseHookOnError validates an on-error token, as config and labels spell it.
func ParseHookOnError(token string) (HookOnError, bool) {
	switch onError := HookOnError(token); onError {
	case HookOnErrorFail, HookOnErrorContinue:
		return onError, true
	default:
		return "", false
	}
}

// ImageBackendKind names the layout a writable layer is stored in. It travels in
// the savepoint record, so its values are the wire contract.
type ImageBackendKind string

const (
	// ImageBackendDedupCopy splits the archive into the shared blob store.
	ImageBackendDedupCopy ImageBackendKind = "dedupcopy"
	// ImageBackendFullCopy stores the archive whole.
	ImageBackendFullCopy ImageBackendKind = "fullcopy"
)
