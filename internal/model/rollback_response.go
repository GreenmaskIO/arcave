package model

// RollbackResponse is the JSON printed by 'arcave rollback'. Restored names each
// unit by the same object id the storage layout is keyed by.
type RollbackResponse struct {
	RolledBackTo Savepoint `json:"rolled_back_to"`
	Restored     []Volume  `json:"restored"`
}
