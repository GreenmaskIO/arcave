package model

import (
	"errors"
)

// Sentinel errors that cross a layer seam: a service returns one, an adapter
// matches it with errors.Is. They live here so neither side imports the other.
var (
	// ErrRemoveSelectorConflict is returned when a remove request combines
	// explicit savepoint IDs with retention rules; the two modes are exclusive.
	ErrRemoveSelectorConflict = errors.New("cannot combine explicit savepoint IDs with retention rules")
	// ErrRemoveNoSelector is returned when a remove request provides neither
	// explicit savepoint IDs nor any retention rule.
	ErrRemoveNoSelector = errors.New("no savepoint selector provided")
	// ErrRetainRecentNegative is returned when the retain-recent count is negative.
	ErrRetainRecentNegative = errors.New("retain-recent count must be zero or positive")

	// ErrInvalidSavepointID reports an id that is not a run of decimal digits.
	// SavepointID.Validate wraps it with the offending id.
	ErrInvalidSavepointID = errors.New("invalid savepoint id")
)
