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

// newServiceVolumes renders the volumes: of one service, every mount type included:
// a mount arcave does not capture still has to come back.
func newServiceVolumes(mounts []model.ServiceVolume) []types.ServiceVolumeConfig {
	if len(mounts) == 0 {
		return nil
	}
	volumes := make([]types.ServiceVolumeConfig, 0, len(mounts))
	for _, mount := range mounts {
		volumes = append(volumes, newServiceVolume(mount))
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
	for _, mount := range svc.Volumes {
		if mount.Type != model.VolumeTypeAnon || mount.Replica == first {
			declared = append(declared, mount)
		}
	}
	return declared
}

// newServiceVolume renders one mount. An anonymous volume renders as a bare target:
// naming the one Down orphaned would make compose reuse it instead of a fresh one.
func newServiceVolume(mount model.ServiceVolume) types.ServiceVolumeConfig {
	config := types.ServiceVolumeConfig{
		Type:     mount.RawType,
		Source:   mount.Source,
		Target:   mount.Target,
		ReadOnly: mount.ReadOnly,
	}

	switch mount.Type {
	case model.VolumeTypeAnon:
		config.Source = ""
		config.Volume = newVolumeOptions(mount.Volume)
	case model.VolumeTypeNamed:
		config.Volume = newVolumeOptions(mount.Volume)
	case model.VolumeTypeBind:
		config.Bind = newBindOptions(mount.Bind)
	case model.VolumeTypeTmpfs:
		config.Source = ""
		if mount.Tmpfs != nil {
			config.Tmpfs = &types.ServiceVolumeTmpfs{
				Size: types.UnitBytes(mount.Tmpfs.SizeBytes),
				Mode: mount.Tmpfs.Mode,
			}
		}
	case model.VolumeTypeImage:
		if mount.Image != nil {
			config.Image = &types.ServiceVolumeImage{SubPath: mount.Image.Subpath}
		}
	}
	return config
}

func newVolumeOptions(from *model.VolumeMount) *types.ServiceVolumeVolume {
	if from == nil {
		return nil
	}
	return &types.ServiceVolumeVolume{
		NoCopy:  from.NoCopy,
		Subpath: from.Subpath,
		Labels:  types.Mapping(from.Labels),
	}
}

// newBindOptions renders a bind's options. With none reported, compose creates a missing
// host path, as it does for a bind declared with no options at all.
func newBindOptions(from *model.BindMount) *types.ServiceVolumeBind {
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
func ownVolumes(mounts []model.ServiceVolume) []model.ServiceVolume {
	own := make([]model.ServiceVolume, 0, len(mounts))
	for _, mount := range mounts {
		if mount.InheritedFrom == "" {
			own = append(own, mount)
		}
	}
	return own
}

// toTmpfsList renders the tmpfs: shorthand, which the daemon reports apart from the
// mounts. A target already mounted is skipped: compose would declare it twice.
func toTmpfsList(mounts map[string]string, declared []types.ServiceVolumeConfig) types.StringList {
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
