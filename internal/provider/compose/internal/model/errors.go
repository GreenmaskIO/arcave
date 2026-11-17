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
)
