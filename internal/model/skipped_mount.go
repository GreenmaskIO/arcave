package model

// SkippedMount is a mount GM cannot snapshot (tmpfs and other non-persistent
// mount types), reported so callers can warn about state that is not captured.
type SkippedMount struct {
	Service string
	Reason  string
}
