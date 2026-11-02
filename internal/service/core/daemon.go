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

package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/system"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// podmanEngine is the component Podman's `docker version` names itself by.
const podmanEngine = "Podman Engine"

// requireDaemon refuses a Docker daemon older than minMajor before a workflow
// touches anything. The floor is Docker's: another engine, or a version it cannot
// read, passes with a warning; the capture refuses an archive it cannot store.
func requireDaemon(ctx context.Context, info system.Info, version types.Version, minMajor int) error {
	if minMajor == 0 {
		return nil
	}
	if engine, ok := newOtherEngine(version); ok {
		zerolog.Ctx(ctx).Warn().Str("engine", engine).Str("server_version", info.ServerVersion).
			Msgf("the daemon is not Docker; arcave needs what Docker %d's image export writes, "+
				"assuming the engine writes it too", minMajor)
		return nil
	}
	major, ok := daemonMajor(info.ServerVersion)
	if !ok {
		zerolog.Ctx(ctx).Warn().Str("server_version", info.ServerVersion).
			Msg("cannot read the daemon version: the engine may not be Docker; " +
				"assuming its image export is supported")
		return nil
	}
	if major < minMajor {
		return fmt.Errorf("%w: the daemon reports version %s; arcave needs Docker %d or newer",
			model.ErrUnsupportedDaemon, info.ServerVersion, minMajor)
	}
	return nil
}

// newOtherEngine names the engine a version's components say is not Docker.
func newOtherEngine(version types.Version) (string, bool) {
	for _, component := range version.Components {
		if component.Name == podmanEngine {
			return component.Name + " " + component.Version, true
		}
	}
	return "", false
}

// daemonMajor reads the major of a Docker engine version ("24.0.7" → 24).
func daemonMajor(version string) (int, bool) {
	head, _, _ := strings.Cut(version, ".")
	major, err := strconv.Atoi(head)
	if err != nil {
		return 0, false
	}
	return major, true
}
