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

	"github.com/compose-spec/compose-go/v2/format"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/docker/api/types/container"

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

// declaredVolumes are the mounts the first replica declares: a later replica's anonymous
// volume is its own copy of the same declaration.
func declaredVolumes(svc model.Service) []model.ServiceVolume {
	if len(svc.Containers) == 0 {
		return svc.Volumes
	}
	first := svc.Containers[0].Number
	declared := make([]model.ServiceVolume, 0, len(svc.Volumes))
	for _, v := range svc.Volumes {
		if v.Type != model.VolumeTypeAnon || v.Replica == first {
			declared = append(declared, v)
		}
	}
	return declared
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

// bindOptions renders a bind's options. With none reported, compose creates a missing
// host path, as it does for a bind declared with no options at all.
func bindOptions(from *model.BindMount) *types.ServiceVolumeBind {
	bind := &types.ServiceVolumeBind{CreateHostPath: true}
	if from == nil {
		return bind
	}
	bind.CreateHostPath = from.CreateHostPath
	bind.SELinux = from.SELinux
	bind.Recursive = from.Recursive
	if from.Propagation != defaultBindPropagation {
		bind.Propagation = from.Propagation
	}
	return bind
}

// ownVolumes drops the mounts a container inherited through volumes_from: declared as its own,
// they would be created afresh rather than shared.
func ownVolumes(volumes []types.ServiceVolumeConfig, inspect container.InspectResponse) []types.ServiceVolumeConfig {
	if inspect.ContainerJSONBase == nil || inspect.HostConfig == nil || len(inspect.HostConfig.VolumesFrom) == 0 {
		return volumes
	}
	requested := requestedTargets(inspect)
	own := make([]types.ServiceVolumeConfig, 0, len(volumes))
	for _, v := range volumes {
		if requested[v.Target] {
			own = append(own, v)
		}
	}
	if len(own) == 0 {
		return nil
	}
	return own
}

// requestedTargets are the targets the container asked to mount itself: through the mount
// API, the bind API, its image's or its own VOLUME, or tmpfs.
func requestedTargets(inspect container.InspectResponse) map[string]bool {
	requested := map[string]bool{}
	for _, m := range inspect.HostConfig.Mounts {
		requested[m.Target] = true
	}
	for _, bind := range inspect.HostConfig.Binds {
		if parsed, err := format.ParseVolume(bind); err == nil {
			requested[parsed.Target] = true
		}
	}
	for target := range inspect.HostConfig.Tmpfs {
		requested[target] = true
	}
	if inspect.Config != nil {
		for target := range inspect.Config.Volumes {
			requested[target] = true
		}
	}
	return requested
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
