package cdc

import (
	"encoding/json"
	"errors"
	"fmt"
)

// errInvalidSnapshotRef reports a restic snapshot id that is not a run of hex
// digits. A ref arrives from a record on disk and is rendered into a storage key
// and a container's command line, so it is gated first.
var errInvalidSnapshotRef = errors.New("invalid restic snapshot id")

// Payload is what a cdc capture reports in its SnapshotStat, and the only way a
// restore can address its data: a restic snapshot id is content-derived, so
// unlike a fullcopy archive key it cannot be recomputed.
//
// The two sizes stay separate: DataAdded is what this capture cost the store
// (near zero for an increment), TotalBytesProcessed the volume's logical size.
type Payload struct {
	SnapshotRef         string `json:"restic_snapshot_id"`
	DataAdded           int64  `json:"data_added"`
	TotalBytesProcessed int64  `json:"total_bytes_processed"`
}

// decodePayload reads back what a capture reported. A record that has been
// through the store arrives as a map[string]any, so the JSON round trip is the
// normal path, not a fallback.
func decodePayload(v any) (Payload, error) {
	if p, ok := v.(Payload); ok {
		return p, nil
	}
	if v == nil {
		return Payload{}, errors.New("no cdc payload: this capture reported none")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return Payload{}, fmt.Errorf("re-encode cdc payload: %w", err)
	}
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Payload{}, fmt.Errorf("decode cdc payload: %w", err)
	}
	return p, nil
}

// validateSnapshotRef admits a non-empty run of lowercase hex — an allowlist,
// like SavepointID.Validate.
func validateSnapshotRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("%w: empty", errInvalidSnapshotRef)
	}
	for _, r := range ref {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return fmt.Errorf("%w: %q", errInvalidSnapshotRef, ref)
		}
	}
	return nil
}
