package model

import "time"

// The label a rollback stamps its safety snapshot with — the only thing telling
// it from an ordinary savepoint, and named by the service, the backends and both
// adapters alike.
const (
	SafetySnapshotLabel      = "arcave-safety-snapshot"
	SafetySnapshotLabelValue = "true"
)

// Savepoint is the metadata of one consistent snapshot of every registered
// stateful volume. The per-volume bookkeeping lives with the SavepointRecord
// that embeds this type. Its json tags are the wire contract: single-savepoint
// command payloads marshal this type directly.
type Savepoint struct {
	ID          SavepointID       `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Labels      map[string]string `json:"labels,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`

	// Artifacts is one entry per unit the capture covered, and Skipped one per
	// unit it deliberately did not. Both are runtime-neutral: what a unit *is*
	// lives in Artifact.Spec, which arcave never parses.
	Artifacts []Artifact    `json:"artifacts,omitempty"`
	Skipped   []SkippedUnit `json:"skipped,omitempty"`

	// Stats is the pre-provider-cut form, still written and read by
	// internal/service/core. Superseded by Artifacts, and gone with core.
	Stats []SnapshotStat `json:"stats,omitempty"`

	// Target is the provider and stack this savepoint was taken from. It is what
	// lets a rollback or a remove resolve the provider that owns the capture,
	// rather than restoring through whatever the config names today — the same
	// reason a rollback already reads the backend out of the record.
	//
	// A pointer, and omitted when absent, so a record written before the provider
	// cut still reads: a nil Target means "the project's configured default", the
	// one legacy branch, exactly as an unnamed config version reads as current.
	Target *Target `json:"target,omitempty"`
}

// IsSafetySnapshot reports whether this record is a rollback's undo rather than
// a savepoint a user asked for.
func (s Savepoint) IsSafetySnapshot() bool {
	return s.Labels[SafetySnapshotLabel] == SafetySnapshotLabelValue
}
