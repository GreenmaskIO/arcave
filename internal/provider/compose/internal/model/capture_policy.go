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

	"github.com/greenmaskio/arcave/internal/model"
)

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
func (m CaptureStamp) String() string {
	fields := []string{strconv.FormatBool(m.Capture)}
	if m.Backend != "" {
		fields = append(fields, fmt.Sprintf("%s=%s", stampLabelBackend, m.Backend))
	}
	fields = append(fields, fmt.Sprintf("%s=%s", stampLabelOrigin, m.Origin))
	if m.Source != "" {
		fields = append(fields, fmt.Sprintf("%s=%s", stampLabelSource, m.Source))
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
	Backend model.SnapshotBackendKind
}

// WritableLayerDecision is a service's writable layer capture decision and the layout
// its replicas' writable layers are stored in.
type WritableLayerDecision struct {
	CaptureDecision
	Backend model.ImageBackendKind
}

// ServiceDecision is what a savepoint takes of one service's writable layers and image, and
// how it holds the service still.
type ServiceDecision struct {
	WritableLayer WritableLayerDecision
	Image         CaptureDecision
	// Mode is empty for CaptureModeStop.
	Mode model.CaptureMode
	// PreCapture and PostCapture run in its replicas around the capture; nil is no hook.
	PreCapture  *Hook
	PostCapture *Hook
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
	VolumeBackend        model.SnapshotBackendKind
	WritableLayerBackend model.ImageBackendKind
	// Mode holds a service no label names; empty for CaptureModeStop.
	Mode     model.CaptureMode
	Services map[string]ServiceDecision
	// NamedVolumes are keyed by docker name.
	NamedVolumes map[string]VolumeDecision
	Mounts       map[MountRef]VolumeDecision
}

// DecideService returns the decision for a service's images; the config's default when
// none was recorded.
func (m CapturePolicy) DecideService(name string) ServiceDecision {
	if d, ok := m.Services[name]; ok {
		return d
	}
	return ServiceDecision{
		WritableLayer: WritableLayerDecision{CaptureDecision: NewConfigDecision(true, ""), Backend: m.WritableLayerBackend},
		Image:         NewConfigDecision(true, ""),
		Mode:          m.Mode,
	}
}

// DecideNamedVolume returns the decision for the named volume of that docker name; the
// config's default when none was recorded.
func (m CapturePolicy) DecideNamedVolume(name string) VolumeDecision {
	if d, ok := m.NamedVolumes[name]; ok {
		return d
	}
	return m.defaultVolume()
}

// DecideMount returns the decision for one bind or anonymous mount; the config's default
// when none was recorded.
func (m CapturePolicy) DecideMount(ref MountRef) VolumeDecision {
	if d, ok := m.Mounts[ref]; ok {
		return d
	}
	return m.defaultVolume()
}

func (m CapturePolicy) defaultVolume() VolumeDecision {
	return VolumeDecision{CaptureDecision: NewConfigDecision(true, ""), Backend: m.VolumeBackend}
}

// NewConfigDecision is a decision the config made; source names the key, empty for the default.
func NewConfigDecision(capture bool, source string) CaptureDecision {
	return CaptureDecision{Capture: capture, Origin: OriginConfig, Source: source}
}
