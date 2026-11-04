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
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
)

const (
	// capabilityPrefix is the kernel's spelling of a capability, which compose does not need.
	capabilityPrefix = "CAP_"
	// noNewPrivileges is the security option an engine may report without its value.
	noNewPrivileges = "no-new-privileges"
	// latestTag is the tag an engine may give an image compose built under a bare name.
	latestTag = ":latest"
)

// linuxSignals names the 31 standard signals by Linux's numbers (x86/ARM), spelled
// out here because the client may run where the numbers differ, such as darwin.
var linuxSignals = map[int]string{
	1: "SIGHUP", 2: "SIGINT", 3: "SIGQUIT", 4: "SIGILL", 5: "SIGTRAP", 6: "SIGABRT",
	7: "SIGBUS", 8: "SIGFPE", 9: "SIGKILL", 10: "SIGUSR1", 11: "SIGSEGV", 12: "SIGUSR2",
	13: "SIGPIPE", 14: "SIGALRM", 15: "SIGTERM", 16: "SIGSTKFLT", 17: "SIGCHLD", 18: "SIGCONT",
	19: "SIGSTOP", 20: "SIGTSTP", 21: "SIGTTIN", 22: "SIGTTOU", 23: "SIGURG", 24: "SIGXCPU",
	25: "SIGXFSZ", 26: "SIGVTALRM", 27: "SIGPROF", 28: "SIGWINCH", 29: "SIGIO", 30: "SIGPWR",
	31: "SIGSYS",
}

// derivedDriverOpts are the volume driver options an engine derives from the o option,
// by the o entry they repeat.
var derivedDriverOpts = map[string]string{"SIZE": "size"}

// normalize spells what an engine reports its own way as compose does; on an engine
// already reporting compose's spelling each step is a no-op.
func normalize(cfg *types.ServiceConfig) {
	cfg.StopSignal = toSignalName(cfg.StopSignal)
	cfg.Ulimits = toUlimitNames(cfg.Ulimits)
	cfg.CapAdd = toCapabilityNames(cfg.CapAdd)
	cfg.CapDrop = toCapabilityNames(cfg.CapDrop)
	cfg.SecurityOpt = toSecurityOptNames(cfg.SecurityOpt)
}

// toSignalName spells a signal reported by its Linux number by name.
func toSignalName(signal string) string {
	number, err := strconv.Atoi(signal)
	if err != nil {
		return signal
	}
	if name, ok := linuxSignals[number]; ok {
		return name
	}
	return signal
}

// toUlimitNames renames RLIMIT_NOFILE-style keys to compose's nofile.
func toUlimitNames(ulimits map[string]*types.UlimitsConfig) map[string]*types.UlimitsConfig {
	if len(ulimits) == 0 {
		return ulimits
	}
	renamed := make(map[string]*types.UlimitsConfig, len(ulimits))
	for name, limit := range ulimits {
		if bare, ok := strings.CutPrefix(name, "RLIMIT_"); ok {
			name = strings.ToLower(bare)
		}
		renamed[name] = limit
	}
	return renamed
}

// toCapabilityNames spells each capability without CAP_, which compose accepts either way.
func toCapabilityNames(capabilities []string) []string {
	if len(capabilities) == 0 {
		return capabilities
	}
	names := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		names = append(names, strings.TrimPrefix(capability, capabilityPrefix))
	}
	return names
}

// toSecurityOptNames spells a bare no-new-privileges with its value, as compose and Docker do.
func toSecurityOptNames(options []string) []string {
	if len(options) == 0 {
		return options
	}
	names := make([]string, 0, len(options))
	for _, option := range options {
		if option == noNewPrivileges {
			option += ":true"
		}
		names = append(names, option)
	}
	return names
}

// dropCPUQuotaOfNanoCPUs drops the quota and period an engine fills in beside a cpus:
// limit when they spell that same limit.
func dropCPUQuotaOfNanoCPUs(resources container.Resources) container.Resources {
	if resources.NanoCPUs <= 0 || resources.CPUPeriod <= 0 ||
		resources.CPUQuota*nanoCPUs != resources.NanoCPUs*resources.CPUPeriod {
		return resources
	}
	resources.CPUQuota, resources.CPUPeriod = 0, 0
	return resources
}

// dropDerivedDriverOpts drops a volume driver option an engine derives from an entry of
// the o option, as SIZE from o: size=10m.
func dropDerivedDriverOpts(options map[string]string) map[string]string {
	if len(options) == 0 {
		return options
	}
	entries := strings.Split(options["o"], ",")
	declared := make(map[string]string, len(options))
	for key, value := range options {
		if name, ok := derivedDriverOpts[key]; ok && slices.Contains(entries, name+"="+value) {
			continue
		}
		declared[key] = value
	}
	return declared
}

// dropLatestOfBuiltImage drops the :latest an engine tags an image compose built under a
// bare name with.
func dropLatestOfBuiltImage(ref string, built image.InspectResponse) string {
	if built.Config == nil || built.Config.Labels[api.ProjectLabel] == "" {
		return ref
	}
	return strings.TrimSuffix(ref, latestTag)
}
