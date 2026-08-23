package model

import (
	"errors"
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

	// ErrInvalidSavepointID reports an id that is not a run of decimal digits.
	// SavepointID.Validate wraps it with the offending id.
	ErrInvalidSavepointID = errors.New("invalid savepoint id")

	// ErrInvalidDigestID reports a string that is not a content digest — a tag, a
	// truncated id, anything the daemon would resolve by name. ParseImageDigestID wraps
	// it with the offending value.
	ErrInvalidDigestID = errors.New("invalid image digest id")

	// ErrSavepointNoSpec refuses a rollback to a savepoint that carries no compose
	// spec: there is nothing to rebuild the stack from.
	ErrSavepointNoSpec = errors.New("savepoint carries no compose spec")

	// ErrSavepointImageMissing reports a record naming a writable-layer capture
	// whose archive the store does not hold.
	ErrSavepointImageMissing = errors.New("savepoint image archive is missing")

	// ErrUnsupportedImageArchive reports that an image archive uses a format
	// this operation does not support (e.g. Docker 24 legacy layout).
	ErrUnsupportedImageArchive = errors.New("unsupported image archive format")
)
