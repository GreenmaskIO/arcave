package model

import "time"

// The label a rollback stamps its safety snapshot with — the only thing telling
// it from an ordinary savepoint, and named by the service, the backends and both
// adapters alike.
const (
	SafetySnapshotLabel      = "arcave-safety-snapshot"
	SafetySnapshotLabelValue = "true"
)

// SpecFormatComposeYAML is the format of a savepoint's spec artifact: a compose
// document, loadable by compose's own loader.
const SpecFormatComposeYAML = "compose-yaml"

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
	Stats       []SnapshotStat    `json:"stats,omitempty"` // one per snapshotted volume
	// Spec names the compose spec committed beside this record. A savepoint without
	// one cannot be rolled back to: the stack is rebuilt from the spec.
	Spec *SpecRef `json:"spec,omitempty"`
}

// SpecRef is the reference to a savepoint's compose spec artifact. Its digest is
// checked before the stack is stopped, so a truncated artifact fails harmlessly.
type SpecRef struct {
	Format string `json:"format"` // SpecFormatComposeYAML
	Digest string `json:"digest"` // sha256 of the artifact, hex
}

// IsSafetySnapshot reports whether this record is a rollback's undo rather than
// a savepoint a user asked for.
func (s Savepoint) IsSafetySnapshot() bool {
	return s.Labels[SafetySnapshotLabel] == SafetySnapshotLabelValue
}
