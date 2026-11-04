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

	"github.com/docker/docker/api/types"

	"github.com/greenmaskio/arcave/internal/model"
)

// versionReader is the one call ReadRuntime makes.
type versionReader interface {
	ServerVersion(ctx context.Context) (types.Version, error)
}

// ReadRuntime asks the daemon which engine it is (model.NewContainerRuntime).
func ReadRuntime(ctx context.Context, api versionReader) (model.ContainerRuntime, error) {
	version, err := api.ServerVersion(ctx)
	if err != nil {
		return model.ContainerRuntime{}, fmt.Errorf("read the daemon version: %w", err)
	}
	return model.NewContainerRuntime(version), nil
}
