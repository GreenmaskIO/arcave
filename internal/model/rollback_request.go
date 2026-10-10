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

import (
	"errors"
	"fmt"
	"time"
)

// ErrRollbackNoSelector is returned when a rollback request names no savepoint
// at all. It crosses the seam — the service returns it and the adapters rephrase
// it in terms of the argument the user typed — so it lives here.
var ErrRollbackNoSelector = errors.New("no savepoint selector provided")

// RollbackRequest carries everything Rollback needs, so callers (CLI, MCP,
// other services) can invoke it without handing over a whole config.Config.
type RollbackRequest struct {
	ID     *SavepointID // exact id; wins over the two below
	Name   *string      // savepoint name; ambiguous names are an error, never a guess
	Latest *bool        // when true, the newest savepoint by CreatedAt

	WaitTimeout time.Duration // bounds the health check on restart; <= 0 lets the compose runner apply its default

	// LeftoverVolumes is what a rollback into a compose project with no containers does with
	// the volumes that still exist; empty means LeftoverVolumesOverwrite.
	LeftoverVolumes LeftoverVolumesPolicy
}

// LeftoverVolumesPolicy is what a rollback does with a volume that outlived its compose
// project's containers.
type LeftoverVolumesPolicy string

const (
	// LeftoverVolumesOverwrite restores over the volume and warns, naming it.
	LeftoverVolumesOverwrite LeftoverVolumesPolicy = "overwrite"
	// LeftoverVolumesRefuse fails the rollback before anything is created.
	LeftoverVolumesRefuse LeftoverVolumesPolicy = "refuse"
)

// Validate refuses a request whose selector is nil or zero-valued, or whose
// leftover-volumes policy is unknown.
func (r RollbackRequest) Validate() error {
	switch {
	case r.ID != nil && *r.ID != "":
	case r.Name != nil && *r.Name != "":
	case r.Latest != nil && *r.Latest:
	default:
		return ErrRollbackNoSelector
	}
	switch r.LeftoverVolumes {
	case "", LeftoverVolumesOverwrite, LeftoverVolumesRefuse:
		return nil
	default:
		return fmt.Errorf("unsupported leftover volumes policy %q", r.LeftoverVolumes)
	}
}

// Ref renders the selector back as the word the user typed, for error messages
// to quote.
func (r RollbackRequest) Ref() string {
	switch {
	case r.ID != nil:
		return r.ID.String()
	case r.Name != nil:
		return *r.Name
	case r.Latest != nil && *r.Latest:
		return RefLatest
	default:
		return ""
	}
}

// RefLatest is the reserved word for the newest savepoint. Both adapters match
// their input against it before building a request.
const RefLatest = "latest"
