package model

// RollbackResponse is the JSON printed by 'arcave rollback'.
type RollbackResponse struct {
	RolledBackTo Savepoint       `json:"rolled_back_to"`
	Restored     []RestoredMount `json:"restored"`
}
