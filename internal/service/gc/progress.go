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

package gc

import (
	"context"
	"fmt"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// The kinds of the steps a gc opens.
const (
	kindGC            progress.Kind = "gc"
	kindSessionLock   progress.Kind = "session.lock"
	kindGCFind        progress.Kind = "gc.find"
	kindGCRetain      progress.Kind = "gc.retain"
	kindGCErase       progress.Kind = "gc.erase"
	kindSavepointDrop progress.Kind = "savepoint.drop"
	kindSavepoint     progress.Kind = "savepoint"
)

// newDeletionError is what a deletion's step, opened on ctx, ends with: nil when every op ran and
// none failed, ctx's cause when its end cut the deletion short.
func newDeletionError(ctx context.Context, deletion *model.GarbageDeletion) error {
	switch {
	case len(deletion.Failed) == 0 && deletion.NotRun == 0:
		return nil
	case deletion.IsInterrupted() && ctx.Err() != nil:
		return context.Cause(ctx)
	}
	return fmt.Errorf("%d deletion(s) failed, %d not run", len(deletion.Failed), deletion.NotRun)
}
