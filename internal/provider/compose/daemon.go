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

package compose

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// requireDaemon refuses a daemon whose version is older than minMajor; 0 requires none.
// A version that cannot be read passes, with a warning.
func requireDaemon(ctx context.Context, version string, minMajor int) error {
	if minMajor == 0 {
		return nil
	}
	major, ok := daemonMajor(version)
	if !ok {
		zerolog.Ctx(ctx).Warn().Str("server_version", version).
			Msg("cannot read the Docker version; assuming its image export is supported")
		return nil
	}
	if major < minMajor {
		return fmt.Errorf("%w: the daemon reports version %s; arcave needs Docker %d or newer",
			model.ErrUnsupportedDaemon, version, minMajor)
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
