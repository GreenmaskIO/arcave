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

import "github.com/moby/moby/api/types/system"

// podmanEngine is the component Podman's `docker version` names itself by.
const podmanEngine = "Podman Engine"

// dockerDesktopOperatingSystem is what `docker info` reports on Docker Desktop, whatever host it runs on.
const dockerDesktopOperatingSystem = "Docker Desktop"

// ContainerRuntimeKind is the engine behind the Docker API arcave talks to.
type ContainerRuntimeKind string

const (
	ContainerRuntimeDocker ContainerRuntimeKind = "docker"
	ContainerRuntimePodman ContainerRuntimeKind = "podman"
)

// ContainerRuntime is the engine a daemon answers as.
type ContainerRuntime struct {
	Kind ContainerRuntimeKind
	// Version is the daemon's version as `docker version` reports it.
	Version string
}

// NewContainerRuntime names the engine a `docker version` answer describes: Podman when
// a component names itself so, Docker otherwise.
func NewContainerRuntime(version string, components []system.ComponentVersion) ContainerRuntime {
	kind := ContainerRuntimeDocker
	for _, component := range components {
		if component.Name == podmanEngine {
			kind = ContainerRuntimePodman
		}
	}
	return ContainerRuntime{
		Kind:    kind,
		Version: version,
	}
}

// IsDockerDesktop reports whether a `docker info` answer is Docker Desktop's: a Docker engine in a
// VM of its own, which is no engine kind of its own.
func IsDockerDesktop(info system.Info) bool {
	return info.OperatingSystem == dockerDesktopOperatingSystem
}
