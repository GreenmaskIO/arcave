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

import "github.com/greenmaskio/arcave/internal/model"

// ComposeProjectResources is everything the daemon holds of a compose project by its label: its
// containers, its replicas among them, and the names of the volumes and networks it owns. An
// external volume carries no such label, nor an anonymous one, which goes with its container.
type ComposeProjectResources struct {
	// Containers are the docker names of every container labelled with the compose project:
	// replicas, one-offs, a lifecycle hook's and a provider's relay, sorted.
	Containers []string
	// Replicas are the service replicas among them by service, sorted by name, what deployment
	// labels are read off.
	Replicas []ServiceReplicas
	Volumes  []string
	Networks []string
}

// ServiceReplicas is one service's replicas, sorted by Number.
type ServiceReplicas struct {
	Name       string
	Containers []Container
}

// NonReplicas are the docker names of the containers that are no replica: one-offs, a lifecycle
// hook's runner, a provider's relay.
func (m ComposeProjectResources) NonReplicas() []string {
	replicas := map[string]bool{}
	for _, service := range m.Replicas {
		for _, replica := range service.Containers {
			replicas[replica.Name] = true
		}
	}
	var names []string
	for _, name := range m.Containers {
		if !replicas[name] {
			names = append(names, name)
		}
	}
	return names
}

// IsEmpty reports whether the daemon holds no container, volume or network of the compose project.
func (m ComposeProjectResources) IsEmpty() bool {
	return len(m.Containers) == 0 && len(m.Volumes) == 0 && len(m.Networks) == 0
}

// Discarded is what dropping the resources removes, every list non-nil.
func (m ComposeProjectResources) Discarded() model.DiscardedResources {
	return model.DiscardedResources{
		Containers: nonNil(m.Containers),
		Volumes:    nonNil(m.Volumes),
		Networks:   nonNil(m.Networks),
	}
}

// nonNil is names, or an empty list for none, so it marshals as [] rather than null.
func nonNil(names []string) []string {
	if names == nil {
		return []string{}
	}
	return names
}
