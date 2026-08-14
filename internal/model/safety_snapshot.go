package model

// The label a rollback stamps its safety snapshot with — the only thing telling
// it from an ordinary savepoint, and named by the service, the backends and both
// adapters alike.
const (
	SafetySnapshotLabel      = "arcave-safety-snapshot"
	SafetySnapshotLabelValue = "true"
)
