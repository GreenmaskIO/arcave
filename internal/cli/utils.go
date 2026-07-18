package cli

import (
	"errors"

	"github.com/greenmaskio/gm-tools/internal/model"
)

// restoredMounts projects the service's mounts onto the wire type, mapping
// Mount.Target to the mount_path the response names it by. The slice is non-nil
// so an empty rollback marshals restored as [] rather than null. Both the CLI
// and MCP rollback handlers share it.
func restoredMounts(ms []model.Mount) []model.RestoredMount {
	out := make([]model.RestoredMount, 0, len(ms))
	for _, m := range ms {
		out = append(out, model.RestoredMount{Service: m.Service, MountPath: m.Target})
	}
	return out
}

// removeUsageError rephrases the service's flag-agnostic selector-validation
// errors in terms of the CLI flags the user typed. Only this layer knows the
// flag names, so the guidance lives here rather than in the service. Both the
// CLI and MCP remove handlers share it.
func removeUsageError(err error) error {
	switch {
	case errors.Is(err, model.ErrRemoveSelectorConflict):
		return errors.New("cannot combine savepoint IDs with --retain-recent/--retain-for/--before-date")
	case errors.Is(err, model.ErrRemoveNoSelector):
		return errors.New("specify savepoint IDs, or at least one of --retain-recent, --retain-for or --before-date")
	case errors.Is(err, model.ErrRetainRecentNegative):
		return errors.New("--retain-recent must be zero or positive")
	default:
		return err
	}
}
