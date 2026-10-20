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

package cli

import (
	"errors"
	"fmt"

	"github.com/greenmaskio/arcave/internal/model"
)

// removeUsageError restates the service's selector-validation errors as the
// fix the caller can act on: which selector to drop, or which to supply.
//
// Both the CLI and MCP remove handlers share it, so the wording names each
// selector without the spelling either adapter gives it — "retain-recent", not
// the CLI's "--retain-recent" nor the tool schema's "retain_recent". A caller
// who typed one recognises the other, and neither is told to pass a flag that
// does not exist on the surface it is using.
func removeUsageError(err error) error {
	switch {
	case errors.Is(err, model.ErrRemoveSelectorConflict):
		return errors.New("cannot combine savepoint ids with retain-recent/retain-for/before-date")
	case errors.Is(err, model.ErrRemoveNoSelector):
		return errors.New("specify savepoint ids, or at least one of retain-recent, retain-for or before-date")
	case errors.Is(err, model.ErrRetainRecentNegative):
		return errors.New("retain-recent must be zero or positive")
	default:
		return err
	}
}

// composeProjectAbsentError adds the fix to a compose project the daemon holds no container of,
// naming the selector as the adapter spells it.
func composeProjectAbsentError(err error, selector string) error {
	if errors.Is(err, model.ErrComposeProjectAbsent) {
		return fmt.Errorf("%w: bring the compose project up once, or name another project with %s", err, selector)
	}
	return err
}
