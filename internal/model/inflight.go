package model

import (
	"fmt"
	"time"
)

// InflightPhase says how far an interrupted rollback got. The remedy differs by phase.
type InflightPhase string

const (
	InflightRestoring    InflightPhase = "restoring"
	InflightRestoreDone  InflightPhase = "restore-done"
	InflightInconsistent InflightPhase = "inconsistent"
)

// InflightMarker is the intent record a rollback writes before its first destructive
// write and clears once the environment is consistent again.
type InflightMarker struct {
	Phase  InflightPhase `json:"phase"`
	Target SavepointID   `json:"target"` // the savepoint being restored
	Safety SavepointID   `json:"safety"` // the undo every phase's remedy points at
	At     time.Time     `json:"at"`
}

// InterruptedRollbackError refuses a mutating workflow while a marker stands. What
// happened and what to do about it is the Marker's; a caller reads it with errors.As.
type InterruptedRollbackError struct {
	Marker InflightMarker
}

func (e *InterruptedRollbackError) Error() string {
	return fmt.Sprintf("rollback to savepoint %s was interrupted (%s)", e.Marker.Target, e.Marker.Phase)
}
