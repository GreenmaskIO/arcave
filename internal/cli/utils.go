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
	case errors.Is(err, model.ErrInvalidRetainFor):
		return fmt.Errorf("%w; retain-for takes a duration, e.g. 24h or 7d", err)
	case errors.Is(err, model.ErrInvalidBeforeDate):
		return fmt.Errorf("%w; before-date takes an RFC 3339 timestamp, e.g. 2026-06-01T00:00:00Z", err)
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
	// defaultDelete is the way past a refusal to delete the default session.
	defaultDelete string
	// cloning is the way past a refusal to delete a tree whose fork looks alive.
	cloning string
	// statusUnknown is the way past a refusal to delete a tree of unknown status.
	statusUnknown string
	// removePartial is the way to finish a session removal that stopped partway.
	removePartial string
}

var (
	cliSessionFixHints = sessionFixHints{
		pointer: "run 'arcave session unset' or 'arcave session set <session>'",
		inUse: "switch with 'arcave session set <session>' or 'arcave session unset' first, " +
			"or pass --force to delete it and clear the pointer",
		defaultDelete: "pass --force to empty it, every savepoint it holds included",
		cloning:       "if no fork is running, pass --force to delete it",
		statusUnknown: "if nothing is writing it, pass --force to delete it",
		removePartial: "run it again with the session id to remove the rest",
	}
	mcpSessionFixHints = sessionFixHints{
		pointer:       "call session_unset or session_set",
		inUse:         "switch with session_set or session_unset first, or pass force to delete it anyway",
		defaultDelete: "pass force to empty it, every savepoint it holds included",
		cloning:       "if no fork is running, pass force to delete it",
		statusUnknown: "if nothing is writing it, pass force to delete it",
		removePartial: "call session_remove again with the session id to remove the rest",
	}
	// mcpPinnedSessionFixHints names force alone for a session in use: a pinned server refuses
	// session_set and session_unset.
	mcpPinnedSessionFixHints = sessionFixHints{
		pointer:       "call session_unset or session_set",
		inUse:         "pass force to delete it anyway",
		defaultDelete: mcpSessionFixHints.defaultDelete,
		cloning:       mcpSessionFixHints.cloning,
		statusUnknown: mcpSessionFixHints.statusUnknown,
		removePartial: mcpSessionFixHints.removePartial,
	}
)

// WithFixHint adds the CLI's fix to a session or an interrupted-rollback error; any other passes
// unchanged.
func WithFixHint(err error) error {
	return recoveryError(sessionFixError(err, cliSessionFixHints), cliRecoveryFixHints)
}

// sessionFixError adds the adapter's fix to a session error.
func sessionFixError(err error, hints sessionFixHints) error {
	switch {
	case errors.Is(err, model.ErrCurrentSessionNotFound), errors.Is(err, model.ErrCurrentSessionUnreadable):
		return fmt.Errorf("%w; %s", err, hints.pointer)
	case errors.Is(err, model.ErrCurrentSessionDelete):
		return fmt.Errorf("%w; %s", err, hints.inUse)
	case errors.Is(err, model.ErrDefaultSessionDelete):
		return fmt.Errorf("%w; %s", err, hints.defaultDelete)
	case errors.Is(err, model.ErrSessionCloning):
		return fmt.Errorf("%w; %s", err, hints.cloning)
	case errors.Is(err, model.ErrSessionStatusUnknown):
		return fmt.Errorf("%w; %s", err, hints.statusUnknown)
	case errors.Is(err, model.ErrSessionRemovePartial):
		return fmt.Errorf("%w; %s", err, hints.removePartial)
	default:
		return err
	}
}

// recoveryFixHints is how an adapter tells a caller to recover from an interrupted or failed
// rollback, each in the spelling of its own surface.
type recoveryFixHints struct {
	// rollback is the rollback that recovers; %[1]s is the session, %[2]s the compose project, %[3]s
	// the savepoint.
	rollback string
	// anySavepoint says a rollback to another savepoint recovers too.
	anySavepoint string
	// safetySnapshot is where to find the safety snapshot a failed revert kept; %s is its id.
	safetySnapshot string
}

// keepRestoredFix is the way to keep a rollback that restored every volume and only did not start;
// %s is the compose project. It names docker, not arcave, so both surfaces share it.
const keepRestoredFix = "or keep the rollback: start the compose project and remove the marker volume labelled " +
	"io.arcave.inflight.compose-project=%s"

var (
	cliRecoveryFixHints = recoveryFixHints{
		rollback:     "recover with `arcave rollback --session %[1]s -p %[2]s %[3]s`",
		anySavepoint: ", or name another savepoint",
		safetySnapshot: "safety snapshot %s lists under `arcave savepoint list --include-safety-snapshots` " +
			"and can be rolled back to by id",
	}
	mcpRecoveryFixHints = recoveryFixHints{
		rollback:       "recover by calling rollback with session %[1]s, compose_project %[2]s and ref %[3]s",
		anySavepoint:   ", or another savepoint",
		safetySnapshot: "savepoint_list with include_safety_snapshots lists safety snapshot %s, which rollback takes by id",
	}
)

// recoveryError adds the way to recover to an error that names a standing inflight marker.
func recoveryError(err error, hints recoveryFixHints) error {
	if interrupted, ok := errors.AsType[*model.InterruptedRollbackError](err); ok {
		return fmt.Errorf("%w; %s", err, newRecoveryFix(interrupted.Marker, hints))
	}
	return err
}

// newRecoveryFix is how to recover from what marker records, as hints spell it.
func newRecoveryFix(marker model.InflightMarker, hints recoveryFixHints) string {
	hint := fmt.Sprintf(hints.rollback, marker.Session, marker.ComposeProject, marker.RecoveryID())
	if marker.AcceptsAnySavepoint() {
		hint += hints.anySavepoint
	}
	switch {
	case marker.Phase == model.InflightInconsistent && marker.Safety != "":
		hint += "; " + fmt.Sprintf(hints.safetySnapshot, marker.Safety)
	case marker.Phase == model.InflightRestoreDone && marker.Safety != "":
		hint += "; " + fmt.Sprintf(keepRestoredFix, marker.ComposeProject)
	}
	return hint
}

// gcInflightFixHints is what a gc refused by an interrupted rollback can still do, as each
// surface spells it.
const (
	cliGCInflightFixHint = "`arcave gc --dry-run` still reports"
	mcpGCInflightFixHint = "call gc with dry_run, which still reports"
)

// gcInflightError adds what a gc refused by an interrupted rollback can still do.
func gcInflightError(err error, hint string) error {
	if _, ok := errors.AsType[*model.InterruptedRollbackError](err); ok {
		return fmt.Errorf("%w; %s", err, hint)
	}
	return err
}

// deploymentFixHints is what an adapter tells a caller to do about an up or a down refused, each
// in the spelling of its own surface.
type deploymentFixHints struct {
	// present is the way past an up over what the daemon still holds of the compose project.
	present string
	// elsewhere is the way past a down of another deployment's environment; %s is its session.
	elsewhere string
	// notByArcave is the way past a down of what no rollback built.
	notByArcave string
	// noSavepoints is how to get a first savepoint.
	noSavepoints string
	// left is the way past a drop something outside the compose project held back.
	left string
}

var (
	cliDeploymentFixHints = deploymentFixHints{
		present: "pass --force to drop all of it first, with no backup; " +
			"'arcave rollback' replaces a running environment and keeps a safety snapshot",
		elsewhere:    "run it in that session with --session %s, or pass --force to take it down anyway",
		notByArcave:  "pass --force to take it down anyway",
		noSavepoints: "start the compose project with 'docker compose up -d', then take one with 'arcave savepoint create'",
		left:         "stop or remove what holds them, then run it again",
	}
	mcpDeploymentFixHints = deploymentFixHints{
		present: "pass force to drop all of it first, with no backup; " +
			"the rollback tool replaces a running environment and keeps a safety snapshot",
		elsewhere:    "call it with session %s, or pass force to take it down anyway",
		notByArcave:  "pass force to take it down anyway",
		noSavepoints: "start the compose project with 'docker compose up -d', then call savepoint_create",
		left:         "stop or remove what holds them, then call it again",
	}
)

// deploymentError adds the fix to what refuses an up or a down, as hints spell it.
func deploymentError(err error, hints deploymentFixHints) error {
	elsewhere, isElsewhere := errors.AsType[*model.DeployedElsewhereError](err)
	switch {
	case errors.Is(err, model.ErrComposeProjectPresent):
		return fmt.Errorf("%w; %s", err, hints.present)
	case isElsewhere:
		return fmt.Errorf("%w; %s", err, fmt.Sprintf(hints.elsewhere, elsewhere.Deployment.Session))
	case errors.Is(err, model.ErrNotDeployedByArcave):
		return fmt.Errorf("%w; %s", err, hints.notByArcave)
	case errors.Is(err, model.ErrNoSavepoints):
		return fmt.Errorf("%w; %s", err, hints.noSavepoints)
	case errors.Is(err, model.ErrComposeProjectLeft):
		return fmt.Errorf("%w; %s", err, hints.left)
	default:
		return err
	}
}
