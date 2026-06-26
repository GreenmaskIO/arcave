// Package dto defines the JSON shapes GM commands print to stdout. It is the
// single source of truth for output structure; both the CLI and MCP adapters
// marshal these types.
package dto

import (
	"time"

	"github.com/greenmaskio/gm-tools/internal/model"
)

// SavepointMeta is the JSON shape printed for a savepoint by create, list, and
// remove.
type SavepointMeta struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// MetaOf maps a domain savepoint to its JSON shape.
func MetaOf(s model.Savepoint) SavepointMeta {
	return SavepointMeta{
		ID:          s.ID,
		Name:        s.Name,
		Description: s.Description,
		CreatedAt:   s.CreatedAt,
	}
}

// MetasOf maps a slice of savepoints to their JSON shapes, always non-nil so it
// renders as [] rather than null.
func MetasOf(ss []model.Savepoint) []SavepointMeta {
	out := make([]SavepointMeta, 0, len(ss))
	for _, s := range ss {
		out = append(out, MetaOf(s))
	}
	return out
}

// RestoredMount is the per-mount entry in the rollback output.
type RestoredMount struct {
	Service   string `json:"service"`
	MountPath string `json:"mount_path"`
}

// RemoveResponse is the JSON printed by 'gm savepoint remove'.
type RemoveResponse struct {
	DryRun  bool            `json:"dry_run"`
	Removed []SavepointMeta `json:"removed"`
}

// RollbackResponse is the JSON printed by 'gm rollback'.
type RollbackResponse struct {
	RolledBackTo SavepointMeta   `json:"rolled_back_to"`
	Restored     []RestoredMount `json:"restored"`
}
