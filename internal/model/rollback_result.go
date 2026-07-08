package model

// RollbackResult reports the savepoint that was restored and which mounts it
// covered.
type RollbackResult struct {
	Savepoint Savepoint
	Restored  []Mount // Service + Target of each restored volume
}
