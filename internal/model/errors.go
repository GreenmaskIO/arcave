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
	// mutating workflow in this project. The service returns it, the lock
	// implementation produces it, and the adapters match it — so it lives here
	// rather than in internal/lock, which core must not import.
	ErrProjectLocked = errors.New("another arcave command is already running in this project")

	// ErrInvalidSavepointID reports an id that is not a run of decimal digits.
	// SavepointID.Validate wraps it with the offending id.
	ErrInvalidSavepointID = errors.New("invalid savepoint id")

	// ErrMissingVolumeInSnapshot reports that a snapshot holds no capture for a
	// volume it is expected to cover; which volumes are missing is logged.
	ErrMissingVolumeInSnapshot = errors.New("snapshot is missing a volume capture")

	// ErrUnsupportedConfigVersion reports a config file whose schema version arcave does
	// not speak. arcave speaks exactly one, so any other value is refused rather than
	// migrated. A file carrying no version at all is taken to be that one version:
	// the pre-v1 flat layout is therefore reported by the keys it uses, not by its
	// missing version.
	ErrUnsupportedConfigVersion = errors.New("unsupported config version")
)
