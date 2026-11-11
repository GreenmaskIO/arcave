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
		return errors.New("cannot combine savepoint ids or names with retain-recent/retain-for/before-date")
	case errors.Is(err, model.ErrRemoveNoSelector):
		return errors.New("specify savepoint ids or names, or at least one of retain-recent, retain-for or before-date")
	case errors.Is(err, model.ErrRetainRecentNegative):
		return errors.New("retain-recent must be zero or positive")
	case errors.Is(err, model.ErrRetainForNegative):
		return errors.New("retain-for must be zero or positive")
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

// sessionFixHints is what an adapter tells a caller to do about the current session.
type sessionFixHints struct {
	// pointer is the way out of a current-session pointer that cannot be read or followed.
	pointer string
	// inUse is the way past a refusal to delete the session in use.
	inUse string
}

var (
	cliSessionFixHints = sessionFixHints{
		pointer: "run 'arcave session unset' or 'arcave session set <session>'",
		inUse: "switch with 'arcave session set <session>' or 'arcave session unset' first, " +
			"or pass --force to delete it and clear the pointer",
	}
	mcpSessionFixHints = sessionFixHints{
		pointer: "call session_unset or session_set",
		inUse:   "switch with session_set or session_unset first, or pass force to delete it anyway",
	}
	// mcpPinnedSessionFixHints names force alone for a session in use: a pinned server refuses
	// session_set and session_unset.
	mcpPinnedSessionFixHints = sessionFixHints{
		pointer: "call session_unset or session_set",
		inUse:   "pass force to delete it anyway",
	}
)

// WithFixHint adds the CLI's fix to a current-session error; any other passes unchanged.
func WithFixHint(err error) error {
	return sessionFixError(err, cliSessionFixHints)
}

// sessionFixError adds the adapter's fix to a current-session error.
func sessionFixError(err error, hints sessionFixHints) error {
	switch {
	case errors.Is(err, model.ErrCurrentSessionNotFound), errors.Is(err, model.ErrCurrentSessionUnreadable):
		return fmt.Errorf("%w; %s", err, hints.pointer)
	case errors.Is(err, model.ErrCurrentSessionDelete):
		return fmt.Errorf("%w; %s", err, hints.inUse)
	default:
		return err
	}
}
