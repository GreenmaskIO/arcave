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

package docker

import (
	"context"
	"fmt"
	"runtime"

	"github.com/docker/docker/api/types/system"
)

// desktopOperatingSystem is how Docker Desktop names itself in the daemon's
// system info, whatever host it runs on.
const desktopOperatingSystem = "Docker Desktop"

// HostLoopbackShared reports whether a container run with host networking
// shares this process's loopback: true only when arcave and a native Linux
// daemon run on one kernel. Docker Desktop and any remote daemon answer false.
func (e *Engine) HostLoopbackShared(ctx context.Context) (bool, error) {
	info, err := e.api.Info(ctx)
	if err != nil {
		return false, fmt.Errorf("read docker daemon info: %w", err)
	}
	return sharesHostLoopback(runtime.GOOS, info), nil
}

// sharesHostLoopback needs the OperatingSystem check even under the GOOS guard:
// arcave inside WSL2 talking to Docker Desktop's daemon is linux on both ends,
// and the daemon's loopback is still the VM's.
func sharesHostLoopback(localOS string, info system.Info) bool {
	return localOS == "linux" && info.OSType == "linux" &&
		info.OperatingSystem != desktopOperatingSystem
}
