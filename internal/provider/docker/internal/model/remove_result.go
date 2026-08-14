package model

// RemoveResult reports what was (or would be) removed.
type RemoveResult struct {
	DryRun  bool
	Removed []Savepoint
}
