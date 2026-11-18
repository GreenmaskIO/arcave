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

package workload

import (
	"context"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
)

// restoredWorkload is the compose project holding the savepoint's data, not running.
type restoredWorkload struct {
	vacant  vacantWorkload
	target  preparedSavepoint
	project *types.Project
}

// Start starts the services and waits on their health checks.
func (r restoredWorkload) Start(ctx context.Context, waitTimeout time.Duration) error {
	return r.vacant.service.deployer.Start(ctx, r.project, waitTimeout)
}

// Reap drops the anonymous volumes the teardown orphaned and the restore replaced, and says where
// the restored writable layers live.
func (r restoredWorkload) Reap(ctx context.Context) {
	r.vacant.service.deployer.Reap(ctx, r.vacant.anonymousVolumeNames, r.target.Target)
}
