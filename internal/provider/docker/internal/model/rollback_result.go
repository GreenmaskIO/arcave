package model

// RollbackResult reports the savepoint that was restored and which volumes it
// covered.
type RollbackResult struct {
	Savepoint Savepoint
	Restored  []Volume // the units restored, as the savepoint recorded them
}
