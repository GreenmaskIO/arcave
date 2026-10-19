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

import "path/filepath"

// MountSourceType says how Source is read.
type MountSourceType string

const (
	MountSourceVolume MountSourceType = "volume" // docker volume name
	MountSourceBind   MountSourceType = "bind"   // host path
)

// MountSource is what a container mounts to reach a volume's data — docker's own
// `type=…,source=…` pair, minus the target the caller picks. The type is not
// decoration: only a docker volume can be asked whether it exists.
type MountSource struct {
	Type   MountSourceType
	Source string
}

// MountSource answers from the compose project alone. ok is false for an
// anonymous volume, whose docker name is created per container — only the daemon
// knows it — and for a bind whose source is missing or relative; both fall back
// to LookupMount.
func (m Volume) MountSource() (MountSource, bool) {
	switch {
	case m.Named != nil:
		if m.Named.VolumeName == "" {
			return MountSource{}, false
		}
		return MountSource{Type: MountSourceVolume, Source: m.Named.VolumeName}, true
	case m.Bind != nil:
		// compose-go resolves a bind source against the project directory, so a
		// relative one means the record never went through it.
		if !filepath.IsAbs(m.Bind.Source) {
			return MountSource{}, false
		}
		return MountSource{Type: MountSourceBind, Source: m.Bind.Source}, true
	default:
		return MountSource{}, false
	}
}

// LookupMount is the mount to ask the daemon about when MountSource cannot answer;
// replica 0 means any replica. ok is false for a Volume with no mount of its own.
func (m Volume) LookupMount() (service string, replica int, target string, ok bool) {
	switch {
	case m.Anon != nil:
		return m.Anon.Service, m.Anon.Replica, m.Anon.Target, true
	case m.Bind != nil:
		return m.Bind.Service, 0, m.Bind.Target, true
	default:
		return "", 0, "", false
	}
}
