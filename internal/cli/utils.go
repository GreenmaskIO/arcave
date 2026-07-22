package cli

import (
	"errors"

	"github.com/greenmaskio/arcave/internal/model"
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

// removeUsageError restates the service's selector-validation errors as the
// remedy the caller can act on: which selector to drop, or which to supply.
//
// Both the CLI and MCP remove handlers share it, so the wording names each
// selector without the spelling either adapter gives it — "retain-recent", not
// the CLI's "--retain-recent" nor the tool schema's "retain_recent". A caller
// who typed one recognises the other, and neither is told to pass a flag that
// does not exist on the surface it is using.
func removeUsageError(err error) error {
	switch {
	case errors.Is(err, model.ErrRemoveSelectorConflict):
		return errors.New("cannot combine savepoint ids with retain-recent/retain-for/before-date")
	case errors.Is(err, model.ErrRemoveNoSelector):
		return errors.New("specify savepoint ids, or at least one of retain-recent, retain-for or before-date")
	case errors.Is(err, model.ErrRetainRecentNegative):
		return errors.New("retain-recent must be zero or positive")
	default:
		return err
	}
}
