// Package mcp is the MCP adapter: it exposes arcave's savepoint workflows as tools
// an AI agent can call natively, mirroring what internal/cli exposes as
// commands. Like that package it declares its own consumer-side view of the
// service and imports no internal/service or internal/client package — a tool
// handler parses its input, builds a model request, calls the service, and
// returns a shared model wire type for the SDK to marshal.
//
// The transport is stdio only: the server runs as a subprocess of its host, so
// stdout *is* the JSON-RPC channel. Nothing here may write to stdout — there is
// no model.Printer in this package, deliberately. Logs go to stderr through
// zerolog, which is safe.
//
// Serialisation is not this package's concern either: core.Service claims the
// project lock itself, so two concurrent mutating tool calls surface
// model.ErrProjectLocked as a tool error, exactly as two concurrent CLI
// invocations do.
package cli

import (
	"context"
	"fmt"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greenmaskio/arcave/internal/model"
)

// serverVersion is the version arcave reports to the MCP host during initialise.
const serverVersion = "0.1.0"

// service is this adapter's view of the savepoint workflows — the same four
// methods internal/cli reaches through one interface per handler. They are
// declared together here because a single Server owns all four tools.
type mcpService interface {
	CreateSavepoint(ctx context.Context, req model.CreateSavepointRequest) (model.Savepoint, error)
	ListSavepoints(ctx context.Context) ([]model.Savepoint, error)
	RemoveSavepoints(ctx context.Context, req model.RemoveRequest) (model.RemoveResult, error)
	Rollback(ctx context.Context, req model.RollbackRequest) (model.RollbackResult, error)
}

// Server serves arcave's savepoint workflows over MCP.
type MCPHandler struct {
	svc          mcpService
	composeFiles []string
}

// New builds the MCP adapter over svc. The compose files are injected rather
// than taken per tool call: they are a property of the resolved project config,
// not something an agent should have to know or could keep in step with it. The
// adapter takes the file list rather than a config.Config because that is all it
// needs, and the whole config would drag internal/config into its import graph.
func NewMCPHandler(svc mcpService, composeFiles []string) *MCPHandler {
	return &MCPHandler{
		svc:          svc,
		composeFiles: composeFiles,
	}
}

// The tool descriptions are written for an agent deciding *when* to call, not
// for a human reading --help: each one says what the tool is for and what it
// costs, since a savepoint stops the stack and a rollback destroys live data.
const (
	descCreate = "Create a savepoint: a consistent snapshot of every stateful volume in the " +
		"project's Docker Compose stack. Call this before a risky change (a schema migration, a " +
		"data transformation, a destructive experiment) so the environment can be restored " +
		"afterwards with the rollback tool. Services are stopped for the duration of the " +
		"snapshot and started again when it completes."
	descList = "List the project's savepoints, newest first, with their id, name, description " +
		"and creation time. Read-only. Call it to find the savepoint id or name to pass to " +
		"rollback or savepoint_remove."
	descRemove = "Delete savepoints and the snapshot data they hold. Select them either by " +
		"explicit ids, or by retention rules (retain_recent/retain_for/before_date) — the two " +
		"modes cannot be combined, and at least one is required. Pass dry_run to see what would " +
		"be deleted without deleting anything. Deletion is permanent."
	descRollback = "Restore the environment to a savepoint, selected by id, by name, or by the " +
		"literal 'latest'. DESTRUCTIVE: every mount it covers is wiped and replaced with the " +
		"savepoint's data, so any change made since that savepoint is lost — including in bind " +
		"mounts, which are the user's own host folders. arcave takes a safety backup of the live " +
		"state first and replays it automatically if the rollback fails, so a failed rollback " +
		"leaves the environment as it was. Services are stopped and started again."
)

// createInput is savepoint_create's input schema. The compose files are not a
// field: they come from the resolved project config, injected into the Server.
type createInput struct {
	Name        string `json:"name,omitempty" jsonschema:"a short name for the savepoint, used to refer to it later; ids are assigned by arcave"`
	Description string `json:"description,omitempty" jsonschema:"what this savepoint captures, e.g. the change it is being taken before"`
	WaitTimeout string `json:"wait_timeout,omitempty" jsonschema:"how long to wait for services to become healthy on restart, e.g. 2m or 90s; omit for the project's default"`
}

// createSavepoint snapshots every stateful mount in the project.
func (h *MCPHandler) createSavepointMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, in createInput) (*mcpsdk.CallToolResult, model.Savepoint, error) {
	waitTimeout, err := parseWaitTimeout(in.WaitTimeout)
	if err != nil {
		return nil, model.Savepoint{}, err
	}
	sp, err := h.svc.CreateSavepoint(ctx, model.CreateSavepointRequest{
		ComposeFiles: h.composeFiles,
		Name:         in.Name,
		Description:  in.Description,
		WaitTimeout:  waitTimeout,
	})
	if err != nil {
		return nil, model.Savepoint{}, err
	}
	return nil, sp, nil
}

// parseWaitTimeout reads the bound a tool call named, in the same unit-bearing
// form the config file uses ("2m", "90s") and retain_for already asks for. An
// omitted field is the zero duration, which the service reads as "the project's
// bound" — so an agent that does not care never has to name one.
func parseWaitTimeout(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("wait_timeout %q: want a unit-bearing value like 2m, 90s or 1h30m", s)
	}
	return d, nil
}

// listOutput wraps the savepoints, because MCP structured output must be a JSON
// object — the CLI prints the bare array this field holds.
type listOutput struct {
	Savepoints []model.Savepoint `json:"savepoints" jsonschema:"the project's savepoints"`
}

// listSavepoints reports every savepoint in the store. It reaches no daemon and
// takes no lock, so it is safe to call while another workflow is running.
// MCP requires an object schema, so it is an empty struct rather than nothing.
func (h *MCPHandler) listSavepointsMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, listOutput, error) {
	sps, err := h.svc.ListSavepoints(ctx)
	if err != nil {
		return nil, listOutput{}, err
	}
	// A non-nil empty slice marshals as [] rather than null, so an agent reading
	// the result never has to tell "no savepoints" from "no such field".
	if sps == nil {
		sps = []model.Savepoint{}
	}
	return nil, listOutput{Savepoints: sps}, nil
}

// removeSavepointInput selects savepoints to remove, either by explicit IDs or by
// retention rules (the two modes are mutually exclusive).
type removeSavepointInput struct {
	IDs          []string `json:"ids,omitempty" jsonschema:"savepoint ids to delete; cannot be combined with the retention fields"`
	RetainRecent int      `json:"retain_recent,omitempty" jsonschema:"keep this many newest savepoints and delete the rest"`
	RetainFor    string   `json:"retain_for,omitempty" jsonschema:"keep savepoints younger than this duration, e.g. 24h or 7d, and delete the rest"`
	BeforeDate   string   `json:"before_date,omitempty" jsonschema:"delete savepoints created before this date (YYYY-MM-DD)"`
	DryRun       bool     `json:"dry_run,omitempty" jsonschema:"report what would be deleted without deleting anything"`
}

// removeSavepoints deletes the selected savepoints and reports what went.
func (h *MCPHandler) removeSavepointsMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, in removeSavepointInput) (*mcpsdk.CallToolResult, model.RemoveResponse, error) {
	res, err := h.svc.RemoveSavepoints(ctx, model.RemoveRequest{
		IDs:          in.IDs,
		RetainRecent: in.RetainRecent,
		RetainFor:    in.RetainFor,
		BeforeDate:   in.BeforeDate,
		DryRun:       in.DryRun,
	})
	if err != nil {
		return nil, model.RemoveResponse{}, removeUsageError(err)
	}
	return nil, model.RemoveResponse(res), nil
}

// rollbackInput is rollback's input schema.
type rollbackInput struct {
	Ref         string `json:"ref" jsonschema:"the savepoint to restore: its id, its name, or the literal 'latest'"`
	WaitTimeout string `json:"wait_timeout,omitempty" jsonschema:"how long to wait for services to become healthy on restart, e.g. 2m or 90s; omit for the project's default"`
}

// rollback restores the environment to the referenced savepoint.
func (h *MCPHandler) rollbackMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, in rollbackInput) (*mcpsdk.CallToolResult, model.RollbackResponse, error) {
	waitTimeout, err := parseWaitTimeout(in.WaitTimeout)
	if err != nil {
		return nil, model.RollbackResponse{}, err
	}
	res, err := h.svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: h.composeFiles,
		Ref:          in.Ref,
		WaitTimeout:  waitTimeout,
	})
	if err != nil {
		return nil, model.RollbackResponse{}, err
	}
	return nil, model.RollbackResponse{
		RolledBackTo: res.Savepoint,
		Restored:     restoredMounts(res.Restored),
	}, nil
}

// Run registers the tools and serves them over stdio until the host closes stdin
// or ctx is cancelled.
func (h *MCPHandler) MCPRun(ctx context.Context) error {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "arcave", Version: serverVersion}, nil)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "savepoint_create", Description: descCreate}, h.createSavepointMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "savepoint_list", Description: descList}, h.listSavepointsMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "savepoint_remove", Description: descRemove}, h.removeSavepointsMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "rollback", Description: descRollback}, h.rollbackMCP)

	return srv.Run(ctx, &mcpsdk.StdioTransport{})
}
