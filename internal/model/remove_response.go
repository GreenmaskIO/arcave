package model

// RemoveResponse is the JSON printed by 'gm savepoint remove'.
type RemoveResponse struct {
	DryRun  bool        `json:"dry_run"`
	Removed []Savepoint `json:"removed"`
}
