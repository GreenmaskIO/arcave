package model

// RollbackResult reports the savepoint that was restored, which volumes it
// covered and which writable layers were put back.
type RollbackResult struct {
	Savepoint Savepoint
	Restored  []Volume       // the units restored, as the savepoint recorded them
	Images    []ImageRestore // the writable layers loaded back into the daemon
}
