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
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/container"

	"github.com/greenmaskio/arcave/internal/model"
)

// applyLinks declares the links compose wrote back as links: and external_links:.
func (a assembly) applyLinks(cfg *types.ServiceConfig, replica model.Container) {
	cfg.Links, cfg.ExternalLinks = a.declaredLinks(linkEntries(replica.Inspect), replica.Number)
}

// linkEntries are the links the daemon reports: per endpoint on a user network, and in
// HostConfig, as /target:/self/alias, on the default bridge.
func linkEntries(inspect container.InspectResponse) []string {
	var entries []string
	if inspect.ContainerJSONBase != nil && inspect.HostConfig != nil {
		entries = append(entries, inspect.HostConfig.Links...)
	}
	if inspect.NetworkSettings != nil {
		for _, endpoint := range inspect.NetworkSettings.Networks {
			if endpoint != nil {
				entries = append(entries, endpoint.Links...)
			}
		}
	}
	return entries
}

// declaredLinks maps each entry's target container to its service, dropping the aliases
// compose adds itself; a target outside the project is an external link. Sorted, deduplicated.
func (a assembly) declaredLinks(entries []string, number int) (links, external []string) {
	for _, entry := range entries {
		target, alias, ok := splitLink(entry)
		if !ok {
			continue
		}
		service, inProject := a.serviceByName[target]
		if !inProject {
			if spec := linkSpec(target, alias); !slices.Contains(external, spec) {
				external = append(external, spec)
			}
			continue
		}
		if a.isGeneratedAlias(alias, target, service, number) {
			continue
		}
		if spec := linkSpec(service, alias); !slices.Contains(links, spec) {
			links = append(links, spec)
		}
	}
	sort.Strings(links)
	sort.Strings(external)
	return links, external
}

// splitLink reads target:alias, or the daemon's legacy /target:/self/alias.
func splitLink(entry string) (target, alias string, ok bool) {
	target, alias, ok = strings.Cut(strings.TrimPrefix(entry, "/"), ":")
	alias = alias[strings.LastIndex(alias, "/")+1:]
	return target, alias, ok && target != "" && alias != ""
}

// isGeneratedAlias reports an alias compose adds to every link: the target's own name, and
// <service>-<n> and <project>-<service>-<n>, numbered after the linking replica.
func (a assembly) isGeneratedAlias(alias, target, service string, number int) bool {
	numbered := service + api.Separator + strconv.Itoa(number)
	return alias == target || alias == numbered || alias == a.introspection.Project.Name+api.Separator+numbered
}

// linkSpec renders name:alias, or the bare name when the alias repeats it.
func linkSpec(name, alias string) string {
	if alias == name {
		return name
	}
	return name + ":" + alias
}
