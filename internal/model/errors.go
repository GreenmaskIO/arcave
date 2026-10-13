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
	"strings"
)

// Sentinel errors that cross a layer seam: a service returns one, an adapter
// matches it with errors.Is to decide how to present it. They live here, with
// the other types both sides share, so that matching on one never forces the
// adapter to import the service package it is meant to be decoupled from.
//
// The message states the domain condition only; how to phrase it in terms of
// the CLI flags the user typed is the adapter's concern, so the service stays
// flag-agnostic.
//
// Only errors of arcave's own seams live here. A sentinel a library owns is
// matched where it is defined — storages.ErrFileNotFound, like io.EOF — because
// the alternative is restating a contract arcave does not control.
var (
	// ErrRemoveSelectorConflict is returned when a remove request combines
	// explicit savepoint IDs with retention rules; the two modes are exclusive.
	ErrRemoveSelectorConflict = errors.New("cannot combine explicit savepoint IDs with retention rules")
	// ErrRemoveNoSelector is returned when a remove request provides neither
	// explicit savepoint IDs nor any retention rule.
	ErrRemoveNoSelector = errors.New("no savepoint selector provided")
	// ErrRetainRecentNegative is returned when the retain-recent count is negative.
	ErrRetainRecentNegative = errors.New("retain-recent count must be zero or positive")
	// ErrProjectLocked is returned when another arcave process is already running a
	// mutating workflow in this project.
	ErrProjectLocked = errors.New("another arcave command is already running in this project")

	// ErrInvalidSavepointID reports an id that is not a canonical UUID.
	// SavepointID.Validate wraps it with the offending id.
	ErrInvalidSavepointID = errors.New("invalid savepoint id")
	// ErrInvalidSavepointName reports a name a savepoint may not carry.
	// CreateSavepointRequest.Validate wraps it with the offending name.
	ErrInvalidSavepointName = errors.New("invalid savepoint name")
	// ErrSavepointNameTaken reports a name another savepoint of the compose project
	// already carries.
	ErrSavepointNameTaken = errors.New("savepoint name is already taken")

	// ErrInvalidSessionID reports a string that is not a session id: a canonical UUID
	// other than the nil one. ParseSessionID wraps it with the offending value.
	ErrInvalidSessionID = errors.New("invalid session id")
	// ErrSessionNotFound reports a session id or name no session record carries.
	ErrSessionNotFound = errors.New("session not found")
	// ErrInvalidSessionName reports a name a session may not carry.
	// CreateSessionRequest.Validate wraps it with the offending name.
	ErrInvalidSessionName = errors.New("invalid session name")
	// ErrSessionNameTaken reports a name another session of the arcave project
	// already carries.
	ErrSessionNameTaken = errors.New("session name is already taken")
	// ErrDefaultSessionDelete refuses to delete DefaultSession, which always exists.
	ErrDefaultSessionDelete = errors.New("the default session cannot be deleted")
	// ErrSessionTreeOccupied refuses a fork into a tree that already holds anything, or a copy
	// of a session onto itself; nothing was written, so the caller must not clean the tree up.
	ErrSessionTreeOccupied = errors.New("session tree is occupied")
	// ErrHeartbeatNotFound reports a tree that holds no heartbeat.
	ErrHeartbeatNotFound = errors.New("heartbeat not found")
	// ErrSessionCloning refuses to delete a tree whose fork kept its heartbeat fresh, unless forced.
	ErrSessionCloning = errors.New("session is still being cloned")
	// ErrSessionStatusUnknown refuses to delete a tree whose heartbeat cannot be read or reports
	// another operation in progress, unless forced.
	ErrSessionStatusUnknown = errors.New("session tree status is unknown")
	// ErrNoSessionRef refuses a command that must name a session but names none.
	ErrNoSessionRef = errors.New("no session named")
	// ErrCurrentSessionNotFound reports a current-session pointer naming a session that no
	// longer exists.
	ErrCurrentSessionNotFound = errors.New("the current session no longer exists")
	// ErrCurrentSessionDelete refuses to delete the session the current-session pointer names, or
	// the one an MCP server is pinned to, unless forced.
	ErrCurrentSessionDelete = errors.New("session is the current session")
	// ErrHeartbeatLost stops an operation whose heartbeat could not be kept fresh: a gc may
	// already have taken its tree for a dead one.
	ErrHeartbeatLost = errors.New("heartbeat lost")

	// ErrInvalidDigestID reports a string that is not a content digest — a tag, a
	// truncated id, anything the daemon would resolve by name. ParseImageDigestID wraps
	// it with the offending value.
	ErrInvalidDigestID = errors.New("invalid image digest id")

	// ErrSavepointNoSpec refuses a rollback to a savepoint that carries no compose
	// spec: there is nothing to rebuild the compose project from.
	ErrSavepointNoSpec = errors.New("savepoint carries no compose spec")

	// ErrSavepointImageMissing reports a record naming a writable-layer capture
	// whose archive the store does not hold.
	ErrSavepointImageMissing = errors.New("savepoint image archive is missing")

	// ErrUnsupportedImageArchive reports that an image archive uses a format
	// this operation does not support (e.g. Docker 24 legacy layout).
	ErrUnsupportedImageArchive = errors.New("unsupported image archive format")

	// ErrLeftoverVolumes refuses a rollback into a compose project that is not running
	// but still owns volumes the rollback would overwrite.
	ErrLeftoverVolumes = errors.New("compose project is not running but its volumes survive")

	// ErrUnsupportedDaemon reports a Docker daemon too old for the image backend.
	ErrUnsupportedDaemon = errors.New("unsupported docker daemon")
)

// AmbiguousSavepointNameError reports a name more than one savepoint carries;
// IDs lists those savepoints.
type AmbiguousSavepointNameError struct {
	Name string
	IDs  []SavepointID
}

func (e *AmbiguousSavepointNameError) Error() string {
	ids := make([]string, len(e.IDs))
	for i, id := range e.IDs {
		ids[i] = id.String()
	}
	return fmt.Sprintf("%d savepoints are named %q; roll back by id instead: %s",
		len(e.IDs), e.Name, strings.Join(ids, ", "))
}

// AmbiguousSessionNameError reports a name more than one session carries; IDs
// lists those sessions.
type AmbiguousSessionNameError struct {
	Name string
	IDs  []SessionID
}

func (e *AmbiguousSessionNameError) Error() string {
	ids := make([]string, len(e.IDs))
	for i, id := range e.IDs {
		ids[i] = id.String()
	}
	return fmt.Sprintf("%d sessions are named %q; name the session by id instead: %s",
		len(e.IDs), e.Name, strings.Join(ids, ", "))
}
