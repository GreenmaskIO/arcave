// Package cli adapts GM's services to Cobra commands: each handler invokes a
// service method and marshals the result to JSON on stdout using the shared
// DTOs. Command definitions and flags live in internal/cmd and delegate here.
package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/gm-tools/internal/adapter/dto"
	"github.com/greenmaskio/gm-tools/internal/client/docker"
	"github.com/greenmaskio/gm-tools/internal/service"
)

// DefaultWaitTimeout is the default bound (seconds) for the rollback health
// check, surfaced here so command definitions need not import the docker client.
const DefaultWaitTimeout = docker.DefaultUpWaitTimeout

// Handlers holds the services the CLI adapter delegates to.
type Handlers struct {
	svc *service.Service
}

// New returns CLI handlers backed by svc.
func New(svc *service.Service) *Handlers {
	return &Handlers{svc: svc}
}

// Init initialises the project and prints a human confirmation line.
func (h *Handlers) Init(cmd *cobra.Command, name string, composeFiles []string, savepointDir string) error {
	resolvedName, path, err := h.svc.Init(cmd.Context(), name, composeFiles, savepointDir)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Initialized GM project %q at %s\n", resolvedName, path)
	return nil
}

// SavepointCreate creates a savepoint and prints its metadata as JSON.
func (h *Handlers) SavepointCreate(cmd *cobra.Command, name, description string) error {
	sp, err := h.svc.CreateSavepoint(cmd.Context(), name, description, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	return writeJSON(cmd.OutOrStdout(), dto.MetaOf(sp))
}

// SavepointList prints all savepoints as a JSON array.
func (h *Handlers) SavepointList(cmd *cobra.Command) error {
	sps, err := h.svc.ListSavepoints()
	if err != nil {
		return err
	}
	return writeJSON(cmd.OutOrStdout(), dto.MetasOf(sps))
}

// SavepointRemove removes savepoints and prints what was (or would be) removed.
func (h *Handlers) SavepointRemove(cmd *cobra.Command, ids []string, hasRecent bool, retainRecent int, retainFor, beforeDate string, dryRun bool) error {
	res, err := h.svc.RemoveSavepoints(service.RemoveRequest{
		IDs:          ids,
		HasRecent:    hasRecent,
		RetainRecent: retainRecent,
		RetainFor:    retainFor,
		BeforeDate:   beforeDate,
		DryRun:       dryRun,
	})
	if err != nil {
		return err
	}
	return writeJSON(cmd.OutOrStdout(), dto.RemoveResponse{
		DryRun:  res.DryRun,
		Removed: dto.MetasOf(res.Removed),
	})
}

// Rollback restores a savepoint and prints what was restored.
func (h *Handlers) Rollback(cmd *cobra.Command, ref string, waitTimeout int) error {
	res, err := h.svc.Rollback(cmd.Context(), ref, waitTimeout, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	restored := make([]dto.RestoredMount, 0, len(res.Restored))
	for _, m := range res.Restored {
		restored = append(restored, dto.RestoredMount{Service: m.Service, MountPath: m.Target})
	}
	return writeJSON(cmd.OutOrStdout(), dto.RollbackResponse{
		RolledBackTo: dto.MetaOf(res.Savepoint),
		Restored:     restored,
	})
}

// writeJSON renders v as indented JSON to w. All gm commands report their
// results as JSON on stdout.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
