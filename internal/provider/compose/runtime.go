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

	"github.com/greenmaskio/arcave/internal/provider/compose/internal/runtimeprofile"
)

// readRuntimeProfile asks the daemon which engine it is and chooses the profile for it.
func readRuntimeProfile(ctx context.Context, daemon daemonConnection) (runtimeprofile.Profile, error) {
	return runtimeprofile.Read(ctx, daemon.api, daemon.cli.DockerEndpoint())
}
