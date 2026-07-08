package model

// RemoveRequest selects savepoints to remove, either by explicit IDs or by
// retention rules (the two modes are mutually exclusive).
type RemoveRequest struct {
	IDs          []string
	RetainRecent int
	RetainFor    string
	BeforeDate   string
	DryRun       bool
}
