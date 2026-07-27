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
	// ErrLegacyManifest is returned when the savepoint store still holds the
	// monolithic manifest.json that per-savepoint records replaced. The adapter
	// renders it with the migration guidance; the repository only reports the
	// condition, since it does not know what the user is allowed to do about it.
	ErrLegacyManifest = errors.New("savepoint store was written by an older arcave and still holds a manifest.json")
	// ErrUnsupportedRecordVersion reports a savepoint record whose schema version
	// arcave does not speak. The version also fixes the storage layout its
	// archives are in, so a foreign one is refused rather than migrated — and,
	// like ErrLegacyManifest, never reported as an empty listing.
	ErrUnsupportedRecordVersion = errors.New("unsupported savepoint record version")

	// ErrUnsupportedConfigVersion reports a config file whose schema version arcave does
	// not speak. arcave speaks exactly one, so any other value is refused rather than
	// migrated. A file carrying no version at all is taken to be that one version:
	// the pre-v1 flat layout is therefore reported by the keys it uses, not by its
	// missing version.
	ErrUnsupportedConfigVersion = errors.New("unsupported config version")
)
