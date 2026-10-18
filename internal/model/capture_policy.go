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
	"fmt"
	"strconv"
	"strings"
)

// LabelPrefix opens every label arcave reads as policy. io.arcave.* is not one:
// those are stamps arcave writes itself, the capture stamps among them.
const LabelPrefix = "arcave."

// The labels a compose project sets its capture policy with.
const (
	// LabelCapture is "true" or "false": on a service, everything of it; on a
	// named or anonymous volume, that volume.
	LabelCapture = "arcave.capture"
	// LabelBackend is a volume's backend token, on a named or anonymous volume.
	LabelBackend = "arcave.backend"
	// LabelVolumeBackend is a service's default backend token for its binds and
	// anonymous volumes.
	LabelVolumeBackend = "arcave.volume.backend"
	// LabelMountsExclude lists, comma-separated, the mount targets of a service no
	// savepoint captures; a named volume's target leaves the volume out.
	LabelMountsExclude = "arcave.mounts.exclude"
	// LabelMountsInclude lists the mount targets of a service a savepoint captures
	// whatever the config says of their kind.
	LabelMountsInclude = "arcave.mounts.include"
	// LabelMountsFullCopy and LabelMountsCDC list the mount targets captured by that backend.
	LabelMountsFullCopy = "arcave.mounts.fullcopy"
	LabelMountsCDC      = "arcave.mounts.cdc"
	// LabelWritableLayerCapture is "true" or "false": a service's containers' writable layers.
	LabelWritableLayerCapture = "arcave.writable-layer.capture"
	// LabelWritableLayerBackend is the layout a service's writable layers are stored in.
	LabelWritableLayerBackend = "arcave.writable-layer.backend"
	// LabelImageCapture is "true" or "false": the bytes of the image a service's image: names.
	LabelImageCapture = "arcave.image.capture"
)

// The stamps a savepoint's spec carries, one per unit: what arcave decided and why. They
// are arcave's statement, never read as policy, and reach the daemon on every container a
// rollback builds.
const (
	// CaptureStampPrefix opens every capture stamp.
	CaptureStampPrefix = "io.arcave.capture."
	// StampWritableLayer is a service's writable layers.
	StampWritableLayer = "io.arcave.capture.writable-layer"
	// StampImage is the image a service's image: names.
	StampImage = "io.arcave.capture.image"
)

// MountStamp is the stamp of the volume or bind a service mounts at target.
func MountStamp(target string) string {
	return fmt.Sprintf("io.arcave.capture.mount.%s", target)
}

// The parts of a stamp label's value: fields after its leading true or false, each
// "<key>=<value>", joined by stampLabelSeparator.
const (
	stampLabelSeparator = "; "
	stampLabelBackend   = "backend"
	stampLabelOrigin    = "origin"
	stampLabelSource    = "source"
)

// CaptureStamp is one stamp's value: "<true|false>[; backend=…]; origin=…[; source=…]".
type CaptureStamp struct {
	Capture bool
	// Backend is the token the unit was captured by; empty when left out.
	Backend string
	Origin  Origin
	Source  string
}

// String renders the stamp's value; the source goes last, so it may hold anything.
func (s CaptureStamp) String() string {
	fields := []string{strconv.FormatBool(s.Capture)}
	if s.Backend != "" {
		fields = append(fields, fmt.Sprintf("%s=%s", stampLabelBackend, s.Backend))
	}
	fields = append(fields, fmt.Sprintf("%s=%s", stampLabelOrigin, s.Origin))
	if s.Source != "" {
		fields = append(fields, fmt.Sprintf("%s=%s", stampLabelSource, s.Source))
	}
	return strings.Join(fields, stampLabelSeparator)
}

// ParseCaptureStamp reads a stamp's value back.
func ParseCaptureStamp(value string) (CaptureStamp, error) {
	head, rest, _ := strings.Cut(value, stampLabelSeparator)
	capture, err := strconv.ParseBool(head)
	if err != nil {
		return CaptureStamp{}, fmt.Errorf("capture stamp %q: want true or false first", value)
	}
	stamp := CaptureStamp{Capture: capture}
	for rest != "" {
		key, afterKey, _ := strings.Cut(rest, "=")
		// The source goes last and may hold the separator, so it takes the rest whole.
		if key == stampLabelSource {
			stamp.Source = afterKey
			break
		}
		var fieldValue string
		fieldValue, rest, _ = strings.Cut(afterKey, stampLabelSeparator)
		switch key {
		case stampLabelBackend:
			stamp.Backend = fieldValue
		case stampLabelOrigin:
			stamp.Origin = Origin(fieldValue)
		default:
			return CaptureStamp{}, fmt.Errorf("capture stamp %q: unknown field %q", value, key)
		}
	}
	return stamp, nil
}

// Origin is where a decision came from: a label the user set, or the config.
type Origin string

const (
	OriginLabel  Origin = "label"
	OriginConfig Origin = "config"
)

// ParseVolumeBackend validates a volume backend token, as config and labels spell it.
func ParseVolumeBackend(token string) (SnapshotBackendKind, bool) {
	switch kind := SnapshotBackendKind(token); kind {
	case SnapshotBackendFullCopy, SnapshotBackendCDC:
		return kind, true
	default:
		return SnapshotBackendUnknown, false
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

// ParseImageBackend validates an image backend token.
func ParseImageBackend(token string) (ImageBackendKind, bool) {
	switch kind := ImageBackendKind(token); kind {
	case ImageBackendDedupCopy, ImageBackendFullCopy:
		return kind, true
	default:
		return "", false
	}
}

// CaptureDecision is whether one unit is captured, where that came from, and what
// said so — "<key>=<value> on <where>" for a label; Source is empty for the default.
type CaptureDecision struct {
	Capture bool
	Origin  Origin
	Source  string
}

// VolumeDecision is a volume's capture decision and the backend it is captured by.
type VolumeDecision struct {
	CaptureDecision
	Backend SnapshotBackendKind
}

// WritableLayerDecision is a service's writable layer capture decision and the layout
// its replicas' writable layers are stored in.
type WritableLayerDecision struct {
	CaptureDecision
	Backend ImageBackendKind
}

// ServiceDecision is what a savepoint takes of one service's writable layers and image.
type ServiceDecision struct {
	WritableLayer WritableLayerDecision
	Image         CaptureDecision
}

// MountRef addresses one bind or anonymous mount: a bind by service and target,
// an anonymous volume by its replica too.
type MountRef struct {
	Service string
	Replica int
	Target  string
}

// CapturePolicy is the capture decision for every resource of a compose project,
// resolved from its labels. A resource it has no entry for is captured by the
// defaults, so the zero maps capture everything.
type CapturePolicy struct {
	VolumeBackend        SnapshotBackendKind
	WritableLayerBackend ImageBackendKind
	Services             map[string]ServiceDecision
	// NamedVolumes are keyed by docker name.
	NamedVolumes map[string]VolumeDecision
	Mounts       map[MountRef]VolumeDecision
}

// DecideService returns the decision for a service's images; the config's default when
// none was recorded.
func (p CapturePolicy) DecideService(name string) ServiceDecision {
	if d, ok := p.Services[name]; ok {
		return d
	}
	return ServiceDecision{
		WritableLayer: WritableLayerDecision{CaptureDecision: ConfigDecision(true, ""), Backend: p.WritableLayerBackend},
		Image:         ConfigDecision(true, ""),
	}
}

// DecideNamedVolume returns the decision for the named volume of that docker name; the
// config's default when none was recorded.
func (p CapturePolicy) DecideNamedVolume(name string) VolumeDecision {
	if d, ok := p.NamedVolumes[name]; ok {
		return d
	}
	return p.defaultVolume()
}

// DecideMount returns the decision for one bind or anonymous mount; the config's default
// when none was recorded.
func (p CapturePolicy) DecideMount(ref MountRef) VolumeDecision {
	if d, ok := p.Mounts[ref]; ok {
		return d
	}
	return p.defaultVolume()
}

func (p CapturePolicy) defaultVolume() VolumeDecision {
	return VolumeDecision{CaptureDecision: ConfigDecision(true, ""), Backend: p.VolumeBackend}
}

// ConfigDecision is a decision the config made; source names the key, empty for the default.
func ConfigDecision(capture bool, source string) CaptureDecision {
	return CaptureDecision{Capture: capture, Origin: OriginConfig, Source: source}
}
