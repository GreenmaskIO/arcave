package model

// RollbackResponse is the JSON printed by 'gm rollback'.
type RollbackResponse struct {
	RolledBackTo Savepoint       `json:"rolled_back_to"`
	Restored     []RestoredMount `json:"restored"`
}
