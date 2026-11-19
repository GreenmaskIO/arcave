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

	"github.com/greenmaskio/arcave/internal/model"
)

var (
	// ErrImageRefNotFound reports an image reference the daemon does not hold.
	ErrImageRefNotFound = errors.New("image reference not found")
	// ErrSavepointNoSpec refuses a rollback to a savepoint that carries no compose
	// spec: there is nothing to rebuild the compose project from.
	ErrSavepointNoSpec = errors.New("savepoint carries no compose spec")
	// ErrSavepointImageMissing reports a record naming a writable-layer capture
	// whose archive the store does not hold.
	ErrSavepointImageMissing = errors.New("savepoint image archive is missing")
	// ErrUnsupportedImageArchive reports that an image archive uses a format
	// this operation does not support (e.g. Docker 24 legacy layout).
	ErrUnsupportedImageArchive = errors.New("unsupported image archive format")
	// ErrInvalidCapturePolicy reports an arcave.* label arcave cannot act on.
	ErrInvalidCapturePolicy = errors.New("invalid capture policy label")
	// ErrContainerNotRunning reports an exec into a container stopped, frozen or gone.
	ErrContainerNotRunning = errors.New("container is not running")

	// ErrInvalidDigestID reports a string that is not a content digest — a tag, a
	// truncated id, anything the daemon would resolve by name. ParseImageDigestID wraps
	// it with the offending value.
	ErrInvalidDigestID = errors.New("invalid image digest id")

	// ErrLeftoverVolumes refuses a rollback into a compose project that is not running
	// but still owns volumes the rollback would overwrite.
	ErrLeftoverVolumes = errors.New("compose project is not running but its volumes survive")

	// ErrUnsupportedDaemon reports a Docker daemon older than arcave supports.
	ErrUnsupportedDaemon = errors.New("unsupported docker daemon")
)

// NotDeployedByArcaveError is what a down cannot put down to a rollback: the replicas with no or
// unreadable deployment labels, as <service>-<number>, or, with no replica left, the containers,
// volumes and networks of the compose project. It is model.ErrNotDeployedByArcave.
type NotDeployedByArcaveError struct {
	Replicas   []string
	Containers []string
	Volumes    []string
	Networks   []string
}

func (m *NotDeployedByArcaveError) Error() string {
	if len(m.Replicas) > 0 {
		return fmt.Sprintf("%v: no deployment labels on %s", model.ErrNotDeployedByArcave, strings.Join(m.Replicas, ", "))
	}
	return fmt.Sprintf("%v: no replica is left to name the owner of %s", model.ErrNotDeployedByArcave,
		model.FormatResources(m.Containers, m.Volumes, m.Networks))
}

func (m *NotDeployedByArcaveError) Unwrap() error { return model.ErrNotDeployedByArcave }

// ComposeProjectLeftError is what a drop of a compose project could not remove: containers, and
// volumes something outside the compose project still holds. Cause is the daemon's refusals of
// those volumes, nil when nothing said why. It is model.ErrComposeProjectLeft.
type ComposeProjectLeftError struct {
	Containers []string
	Volumes    []string
	Cause      error
}

func (m *ComposeProjectLeftError) Error() string {
	msg := fmt.Sprintf("%v: %s", model.ErrComposeProjectLeft, model.FormatResources(m.Containers, m.Volumes, nil))
	if m.Cause != nil {
		msg += ": " + m.Cause.Error()
	}
	return msg
}

func (m *ComposeProjectLeftError) Unwrap() []error {
	return []error{model.ErrComposeProjectLeft, m.Cause}
}
