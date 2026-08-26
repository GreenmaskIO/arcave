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

package composespec

import (
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/greenmaskio/arcave/internal/model"
)

// defaultBindPropagation is what the daemon reports for a bind nobody configured.
// Declaring it would push compose onto the mount API, which creates no missing host path.
const defaultBindPropagation = "rprivate"

// serviceVolumes renders the volumes: of one service, every mount type included:
// a mount arcave does not capture still has to come back.
func serviceVolumes(mounts []model.ServiceVolume) []types.ServiceVolumeConfig {
	if len(mounts) == 0 {
		return nil
	}
	volumes := make([]types.ServiceVolumeConfig, 0, len(mounts))
	for _, m := range mounts {
		volumes = append(volumes, serviceVolume(m))
	}
	return volumes
}

// serviceVolume renders one mount. An anonymous volume renders as a bare target:
// naming the one Down orphaned would make compose reuse it instead of a fresh one.
func serviceVolume(m model.ServiceVolume) types.ServiceVolumeConfig {
	volume := types.ServiceVolumeConfig{
		Type:     m.RawType,
		Source:   m.Source,
		Target:   m.Target,
		ReadOnly: m.ReadOnly,
	}

	switch m.Type {
	case model.VolumeTypeAnon:
		volume.Source = ""
		volume.Volume = volumeOptions(m.Volume)
	case model.VolumeTypeNamed:
		volume.Volume = volumeOptions(m.Volume)
	case model.VolumeTypeBind:
		volume.Bind = bindOptions(m.Bind)
	case model.VolumeTypeTmpfs:
		volume.Source = ""
		if m.Tmpfs != nil {
			volume.Tmpfs = &types.ServiceVolumeTmpfs{
				Size: types.UnitBytes(m.Tmpfs.SizeBytes),
				Mode: m.Tmpfs.Mode,
			}
		}
	case model.VolumeTypeImage:
		if m.Image != nil {
			volume.Image = &types.ServiceVolumeImage{SubPath: m.Image.Subpath}
		}
	}
	return volume
}

func volumeOptions(from *model.VolumeMount) *types.ServiceVolumeVolume {
	if from == nil {
		return nil
	}
	return &types.ServiceVolumeVolume{
		NoCopy:  from.NoCopy,
		Subpath: from.Subpath,
		Labels:  types.Mapping(from.Labels),
	}
}

// bindOptions keeps compose creating a missing host path, as it does for a bind
// declared with no options at all.
func bindOptions(from *model.BindMount) *types.ServiceVolumeBind {
	bind := &types.ServiceVolumeBind{CreateHostPath: true}
	if from == nil {
		return bind
	}
	bind.SELinux = from.SELinux
	bind.Recursive = from.Recursive
	if from.Propagation != defaultBindPropagation {
		bind.Propagation = from.Propagation
	}
	return bind
}

// tmpfsMounts renders the tmpfs: shorthand, which the daemon reports apart from the
// mounts. A target already mounted is skipped: compose would declare it twice.
func tmpfsMounts(mounts map[string]string, declared []types.ServiceVolumeConfig) types.StringList {
	if len(mounts) == 0 {
		return nil
	}
	taken := make(map[string]bool, len(declared))
	for _, v := range declared {
		taken[v.Target] = true
	}

	tmpfs := make([]string, 0, len(mounts))
	for target, options := range mounts {
		if taken[target] {
			continue
		}
		if options == "" {
			tmpfs = append(tmpfs, target)
			continue
		}
		tmpfs = append(tmpfs, strings.Join([]string{target, options}, ":"))
	}
	if len(tmpfs) == 0 {
		return nil
	}
	sort.Strings(tmpfs)
	return tmpfs
}
