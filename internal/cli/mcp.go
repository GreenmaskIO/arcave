// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

// MCPService is the MCP adapter's view of the savepoint workflows.
type MCPService interface {
	CreateSavepoint(ctx context.Context, req model.CreateSavepointRequest) (model.Savepoint, error)
	ListSavepoints(ctx context.Context, req model.ListRequest) ([]model.Savepoint, error)
	RemoveSavepoints(ctx context.Context, req model.RemoveRequest) (model.RemoveResult, error)
	Rollback(ctx context.Context, req model.RollbackRequest) (model.RollbackResult, error)
}

// MCPServiceFactory builds the workflows for one compose project, plus the release
// the tool call invokes on return.
type MCPServiceFactory func(ctx context.Context, project string) (svc MCPService, release func(), err error)

// MCPHandler serves arcave's savepoint workflows as MCP tools over stdio.
// stdout is the JSON-RPC channel, so nothing here may write to it; logs go to stderr.
type MCPHandler struct {
	serviceFactory MCPServiceFactory
}

// NewMCPHandler builds the MCP adapter; the stack a call operates on arrives with the call.
func NewMCPHandler(serviceFactory MCPServiceFactory) *MCPHandler {
	return &MCPHandler{serviceFactory: serviceFactory}
}

// The tool descriptions are written for an agent deciding *when* to call, not
// for a human reading --help: each one says what the tool is for and what it
// costs, since a savepoint stops the stack and a rollback destroys live data.
const (
	descCreate = "Create a savepoint: a consistent snapshot of every stateful volume in the " +
		"project's Docker Compose stack. Call this before a risky change (a schema migration, a " +
		"data transformation, a destructive experiment) so the environment can be restored " +
		"afterwards with the rollback tool. Services are stopped for the duration of the " +
		"snapshot and started again when it completes. If the call fails with a message of the " +
		"form 'savepoint <id> committed; services failed to start', the savepoint EXISTS and is " +
		"safe to roll back to — only the restart failed, so report that id to the user and " +
		"investigate the stack (the services are stopped or unhealthy), rather than taking " +
		"another savepoint."
	descList = "List the project's savepoints, newest first, with their id, name, description " +
		"and creation time. Read-only. Call it to find the savepoint id or name to pass to " +
		"rollback or savepoint_remove. Safety snapshots — the undo a rollback commits of the live " +
		"state before it overwrites it — are hidden unless include_safety_snapshots is set: one " +
		"exists only while a rollback runs, and one that outlived its rollback means that rollback " +
		"was killed partway. That leftover is the last copy of the pre-rollback state, so list it " +
		"with the flag and roll back to it by id to recover by hand."
	descRemove = "Delete savepoints and the snapshot data they hold. Select them either by " +
		"explicit ids, or by retention rules (retain_recent/retain_for/before_date) — the two " +
		"modes cannot be combined, and at least one is required. Pass dry_run to see what would " +
		"be deleted without deleting anything. Deletion is permanent."
	descRollback = "Restore the environment to a savepoint, selected by id, by name, or by the " +
		"literal 'latest'. DESTRUCTIVE: every volume it covers is wiped whole and replaced with " +
		"the savepoint's data, so any change made since that savepoint is lost — including in " +
		"bind mounts, which are the user's own host folders, and including the parts of a volume " +
		"a service only sees a subpath of. arcave takes a safety snapshot of the live state first " +
		"and replays it automatically if the rollback fails, so a failed rollback leaves the " +
		"environment as it was. Services are stopped and started again."
)

// createInput is savepoint_create's input schema.
type createInput struct {
	Project     string `json:"project" jsonschema:"the docker compose project to operate on, as docker compose ls names it"`
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
	svc, release, err := h.serviceFactory(ctx, in.Project)
	if err != nil {
		return nil, model.Savepoint{}, err
	}
	defer release()

	sp, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{
		Name:        in.Name,
		Description: in.Description,
		WaitTimeout: waitTimeout,
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

// listSavepointInput is list's input schema — one flag, and the tool is
// otherwise argument-free.
type listSavepointInput struct {
	Project                string `json:"project" jsonschema:"the docker compose project to operate on, as docker compose ls names it"`
	IncludeSafetySnapshots bool   `json:"include_safety_snapshots,omitempty" jsonschema:"also list the safety snapshots a rollback commits for its own undo; hidden by default"`
}

// listSavepoints reports the savepoints in the store. It reaches no daemon and
// takes no lock, so it is safe to call while another workflow is running.
func (h *MCPHandler) listSavepointsMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, in listSavepointInput) (*mcpsdk.CallToolResult, listOutput, error) {
	svc, release, err := h.serviceFactory(ctx, in.Project)
	if err != nil {
		return nil, listOutput{}, err
	}
	defer release()

	sps, err := svc.ListSavepoints(ctx, model.ListRequest{IncludeSafetySnapshots: in.IncludeSafetySnapshots})
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
	Project      string   `json:"project" jsonschema:"the docker compose project to operate on, as docker compose ls names it"`
	IDs          []string `json:"ids,omitempty" jsonschema:"savepoint ids to delete; cannot be combined with the retention fields"`
	RetainRecent int      `json:"retain_recent,omitempty" jsonschema:"keep this many newest savepoints and delete the rest"`
	RetainFor    string   `json:"retain_for,omitempty" jsonschema:"keep savepoints younger than this duration, e.g. 24h or 7d, and delete the rest"`
	BeforeDate   string   `json:"before_date,omitempty" jsonschema:"delete savepoints created before this date (YYYY-MM-DD)"`
	DryRun       bool     `json:"dry_run,omitempty" jsonschema:"report what would be deleted without deleting anything"`
}

// removeSavepoints deletes the selected savepoints and reports what went.
func (h *MCPHandler) removeSavepointsMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, in removeSavepointInput) (*mcpsdk.CallToolResult, model.RemoveResponse, error) {
	svc, release, err := h.serviceFactory(ctx, in.Project)
	if err != nil {
		return nil, model.RemoveResponse{}, err
	}
	defer release()

	res, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{
		IDs:          model.SavepointIDs(in.IDs),
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
	Project     string `json:"project" jsonschema:"the docker compose project to operate on, as docker compose ls names it"`
	Ref         string `json:"ref" jsonschema:"the savepoint to restore: its id, its name, or the literal 'latest'"`
	WaitTimeout string `json:"wait_timeout,omitempty" jsonschema:"how long to wait for services to become healthy on restart, e.g. 2m or 90s; omit for the project's default"`
}

// rollback restores the environment to the referenced savepoint.
func (h *MCPHandler) rollbackMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, in rollbackInput) (*mcpsdk.CallToolResult, model.RollbackResponse, error) {
	waitTimeout, err := parseWaitTimeout(in.WaitTimeout)
	if err != nil {
		return nil, model.RollbackResponse{}, err
	}
	svc, release, err := h.serviceFactory(ctx, in.Project)
	if err != nil {
		return nil, model.RollbackResponse{}, err
	}
	defer release()

	res, err := svc.Rollback(ctx, newRollbackRequest(in.Ref, waitTimeout))
	if err != nil {
		return nil, model.RollbackResponse{}, err
	}
	return nil, model.RollbackResponse{
		RolledBackTo: res.Savepoint,
		Restored:     res.Restored,
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
