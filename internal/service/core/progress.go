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

	"github.com/greenmaskio/arcave/pkg/progress"
)

// The kinds of the steps core opens: <namespace>.<action> for an action, a bare noun for an object.
const (
	kindSavepointCreate progress.Kind = "savepoint.create"
	kindSavepointRemove progress.Kind = "savepoint.remove"
	kindSavepointDrop   progress.Kind = "savepoint.drop"
	kindSavepoint       progress.Kind = "savepoint"

	kindRollback               progress.Kind = "rollback"
	kindRollbackPrepare        progress.Kind = "rollback.prepare"
	kindRollbackSafetySnapshot progress.Kind = "rollback.safety-snapshot"
	kindRollbackApply          progress.Kind = "rollback.apply"
	kindRollbackRevert         progress.Kind = "rollback.revert"
	kindRollbackCleanup        progress.Kind = "rollback.cleanup"

	kindSessionRemove progress.Kind = "session.remove"
	kindSessionLock   progress.Kind = "session.lock"
	kindSessionFork   progress.Kind = "session.fork"
)

// runStep runs fn under a workflow step of kind, ended with fn's error.
func runStep[T any](ctx context.Context, kind progress.Kind, fn func(ctx context.Context) (T, error)) (_ T, err error) {
	ctx, step := progress.Begin(ctx, progress.Step{Kind: kind})
	defer func() { step.End(err) }()
	return fn(ctx)
}
