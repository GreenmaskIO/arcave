package model

// One request per kind of backend operation, tier-blind: the tier is the
// instance's, not the call's. The Backend prefix keeps them apart from the
// adapter-level RemoveRequest/RollbackRequest, and SnapshotID addresses either
// kind of capture — a savepoint's or a safety backup's.
type (
	BackendSnapshotRequest struct {
		SnapshotID SavepointID
		// Labels are the savepoint's marks, for the backend to render into
		// whatever marking its own store understands. A backend with nowhere to
		// put them ignores them.
		Labels map[string]string
	}

	BackendRestoreRequest struct {
		SnapshotID SavepointID
		// Payload is the SnapshotStat.Payload the write reported; only the backend
		// that produced it interprets it.
		Payload any
	}

	// BackendExistsRequest carries no payload: id and volume address a capture on
	// their own.
	BackendExistsRequest struct {
		SnapshotID SavepointID
	}

	BackendRemoveRequest struct {
		SnapshotID SavepointID
		Payload    any
	}
)
