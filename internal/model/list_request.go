package model

// ListRequest selects which savepoints a listing answers with. A struct rather
// than a bool parameter, like every request type here: the next flag does not
// change the signature.
type ListRequest struct {
	// IncludeSafetySnapshots adds the undo records a rollback commits. Hidden by
	// default: one exists only while a rollback runs, and a leftover is the shard
	// of a killed one.
	IncludeSafetySnapshots bool
}
