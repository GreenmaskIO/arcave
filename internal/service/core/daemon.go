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

	"github.com/docker/docker/api/types/system"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// requireDaemon refuses a daemon older than minMajor before a workflow touches
// anything. A version it cannot read passes with a warning; the capture refuses an
// archive it cannot store.
func requireDaemon(ctx context.Context, info system.Info, minMajor int) error {
	if minMajor == 0 {
		return nil
	}
	major, ok := daemonMajor(info.ServerVersion)
	if !ok {
		zerolog.Ctx(ctx).Warn().Str("server_version", info.ServerVersion).
			Msg("cannot read the daemon version: the engine may not be Docker (Podman and others report " +
				"their own); assuming its image export is supported")
		return nil
	}
	if major < minMajor {
		return fmt.Errorf("%w: the daemon reports version %s; arcave needs Docker %d or newer",
			model.ErrUnsupportedDaemon, info.ServerVersion, minMajor)
	}
	return nil
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
