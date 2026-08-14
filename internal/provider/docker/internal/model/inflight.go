package model

import (
	"fmt"
	"time"

	arcavemodel "github.com/greenmaskio/arcave/internal/model"
)

// InflightMarkerVersion is stamped into every marker, for the day the shape changes.
const InflightMarkerVersion = 1

// InflightPhase says how far an interrupted rollback got. The remedy differs by phase.
type InflightPhase string

const (
	// InflightRestoring: destructive writes were in flight; volumes may mix epochs.
	InflightRestoring InflightPhase = "restoring"
	// InflightRestoreDone: every volume was restored, only the restart was cut short.
	InflightRestoreDone InflightPhase = "restore-done"
	// InflightInconsistent: the revert failed; the safety snapshot is the last copy.
	InflightInconsistent InflightPhase = "inconsistent"
)

// InflightMarker is the intent record a rollback writes before its first destructive
// write and clears once the environment is consistent again.
type InflightMarker struct {
	Version int                     `json:"version"`
	Phase   InflightPhase           `json:"phase"`
	Target  arcavemodel.SavepointID `json:"target"` // the savepoint being restored
	Safety  arcavemodel.SavepointID `json:"safety"` // the undo every phase's remedy points at
	At      time.Time               `json:"at"`
}

// InterruptedRollbackError refuses a mutating workflow while a marker stands. What
// happened and what to do about it is the Marker's; a caller reads it with errors.As.
type InterruptedRollbackError struct {
	Marker InflightMarker
}

func (e *InterruptedRollbackError) Error() string {
	return fmt.Sprintf("rollback to savepoint %s was interrupted (%s)", e.Marker.Target, e.Marker.Phase)
}
