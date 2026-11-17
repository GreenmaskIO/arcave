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

package hold

import (
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// The kinds of the steps the triggers open: compose's stop and start, a line per container, and
// the hooks, a batch per phase with a step per replica under it.
const (
	kindComposeStop     progress.Kind = "compose.stop"
	kindComposeStart    progress.Kind = "compose.start"
	kindHookPreCapture  progress.Kind = "hook.pre-capture"
	kindHookPostCapture progress.Kind = "hook.post-capture"
	kindHook            progress.Kind = "hook"
)

// newHookBatchKind is the kind of the batch of the hooks of phase.
func newHookBatchKind(phase model.HookPhase) progress.Kind {
	if phase == model.HookPhasePostCapture {
		return kindHookPostCapture
	}
	return kindHookPreCapture
}
