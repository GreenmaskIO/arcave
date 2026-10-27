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
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
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

// MCPServiceFactory builds the workflows for one compose project in the session ref names, ""
// being the current one, plus the release the tool call invokes on return.
type MCPServiceFactory func(ctx context.Context, project, session string) (svc MCPService, release func(), err error)

// MCPSessionService is the MCP adapter's view of the session commands.
type MCPSessionService interface {
	ResolveSession(ctx context.Context, ref string) (model.Session, error)
	ListSessions(ctx context.Context, req model.ListSessionsRequest) ([]model.SessionSummary, error)
	CreateSession(ctx context.Context, req model.CreateSessionRequest) (model.Session, error)
	RemoveSession(ctx context.Context, req model.RemoveSessionRequest) (model.RemoveSessionResponse, error)
	SetSession(ctx context.Context, ref string) (model.Session, error)
	UnsetSession(ctx context.Context) (model.Session, error)
}

// MCPSessionServiceFactory builds the session commands, plus the release the tool call invokes on return.
type MCPSessionServiceFactory func(ctx context.Context) (svc MCPSessionService, release func(), err error)

// MCPToolsService is the MCP adapter's view of the tools commands.
type MCPToolsService interface {
	GenerateCompose(ctx context.Context, req model.GenerateComposeRequest) (model.GenerateComposeResponse, error)
}

// MCPToolsServiceFactory builds the tools commands for one compose project, plus the release the
// tool call invokes on return; they open no store and resolve no session.
type MCPToolsServiceFactory func(ctx context.Context, project string) (svc MCPToolsService, release func(), err error)

// MCPGCService is the MCP adapter's view of gc.
type MCPGCService interface {
	GarbageCollect(ctx context.Context, req model.GarbageCollectRequest) (model.GarbageCollectResult, error)
}

// MCPGCServiceFactory builds a gc for one tool call's session ref ("" is the current one),
// plus the release the tool call invokes on return.
type MCPGCServiceFactory func(ctx context.Context, session string) (svc MCPGCService, release func(), err error)

// MCPHandler serves arcave's savepoint and session workflows as MCP tools over stdio.
// stdout is the JSON-RPC channel, so nothing here may write to it; logs go to stderr.
type MCPHandler struct {
	serviceFactory        MCPServiceFactory
	sessionFactory        MCPSessionServiceFactory
	toolsFactory          MCPToolsServiceFactory
	gcFactory             MCPGCServiceFactory
	pinnedSession         model.SessionID
	leftoverVolumesPolicy model.LeftoverVolumesPolicy
}

// NewMCPHandler builds the MCP adapter; pinnedSession is the session the server was started in, zero to
// follow the current one, and leftoverVolumes is the project's rollback.leftover_volumes.
func NewMCPHandler(
	serviceFactory MCPServiceFactory, sessionFactory MCPSessionServiceFactory, toolsFactory MCPToolsServiceFactory,
	gcFactory MCPGCServiceFactory, pinnedSession model.SessionID, leftoverVolumes model.LeftoverVolumesPolicy,
) *MCPHandler {
	return &MCPHandler{
		serviceFactory:        serviceFactory,
		sessionFactory:        sessionFactory,
		toolsFactory:          toolsFactory,
		gcFactory:             gcFactory,
		pinnedSession:         pinnedSession,
		leftoverVolumesPolicy: leftoverVolumes,
	}
}

// openService builds the workflows for project in session, else in the pinned or current session.
func (h *MCPHandler) openService(ctx context.Context, project, session string) (MCPService, func(), error) {
	svc, release, err := h.serviceFactory(ctx, project, cmp.Or(session, h.pinRef()))
	if err != nil {
		return nil, nil, mcpError(err)
	}
	return svc, release, nil
}

// The tool descriptions are written for an agent deciding *when* to call, not
// for a human reading --help: each one says what the tool is for and what it
// costs, since a savepoint stops the compose project and a rollback destroys live data.
const (
	descCreate = "Create a savepoint: a consistent snapshot of every stateful volume of the " +
		"Docker Compose project. Call this before a risky change (a schema migration, a " +
		"data transformation, a destructive experiment) so the environment can be restored " +
		"afterwards with the rollback tool. Services are stopped for the duration of the " +
		"snapshot and started again when it completes. The result's not_covered lists what the " +
		"savepoint does NOT hold — skipped mounts with a reason code, services with no volume, " +
		"containers killed at the stop deadline (their data may be crash-consistent only), replica " +
		"gaps and containers whose image is unknown — so tell the user before relying on " +
		"it. If the call fails with a message of the " +
		"form 'savepoint <id> committed; services failed to start', the savepoint EXISTS and is " +
		"safe to roll back to — only the restart failed, so report that id to the user and " +
		"investigate the compose project (the services are stopped or unhealthy), rather than taking " +
		"another savepoint."
	descList = "List the project's savepoints, newest first unless sort is asc, with their id, name, " +
		"description and creation time. Read-only. Call it to find the savepoint id or name to pass to " +
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
		"literal 'latest'. DESTRUCTIVE: every volume it covers is overwritten with " +
		"the savepoint's data, so any change made since that savepoint is lost — including in " +
		"bind mounts, which are the user's own host folders, and including the parts of a volume " +
		"a service only sees a subpath of. arcave takes a safety snapshot of the live state first " +
		"and replays it automatically if the rollback fails, so a failed rollback leaves the " +
		"environment as it was. Services are stopped and started again."
	descSessionList = "List the arcave project's sessions — independent timelines of savepoints — newest " +
		"first unless sort is asc, with their id, name, savepoint count, compose projects and size_bytes. Read-only. current " +
		"marks the session the other tools work in when they are given no session — the one session_current " +
		"names. all adds session trees " +
		"without a record — forks still cloning, failed or left incomplete — with their status."
	descSessionCreate = "Create a session: empty, or with from a copy (fork) of another session — every " +
		"compose project and savepoint it holds. A fork copies the whole session, so it costs that session's " +
		"size_bytes in time and space, and it touches no container: to run the forked state, call " +
		"session_set (or pass session) and then rollback to one of its savepoints. A fork holds the project " +
		"lock for its whole copy, so every other tool fails with a lock error until it finishes."
	descSessionRemove = "Remove a session and every savepoint it holds, permanently. A compose project " +
		"running from it is left as it is. The default session (baseline) cannot be removed. The current " +
		"session, and the one this server is pinned to, are refused unless force is set, which also resets the current session to baseline; force " +
		"likewise removes a tree whose fork still looks alive — only when no fork is running."
	descSessionSet = "Make a session the current one, by name or id: every later tool call that is given " +
		"no session works in it — and so do the user's own arcave commands in this project directory, " +
		"which share the pointer. session_unset goes back to the default session. Refused when the server " +
		"was started with --session: pass session per call instead."
	descSessionCurrent = "Show the session tool calls work in when they are given no session: the one the " +
		"server was started with --session, else the current session, else the default (baseline). Read-only. " +
		"Fails when the current session no longer exists; session_unset or session_set repairs it."
	descComposeGenerate = "Generate a docker compose file for a running compose project, from what the Docker " +
		"daemon reports about its containers, volumes and networks — for a project whose compose file is lost, " +
		"or was never written. Read-only: nothing is stopped or written, and the file is returned as spec. " +
		"Bind sources under the project's working directory are relative (./…) unless absolute_paths is set. " +
		"The file carries every environment value the containers run with, secrets included."
	descGC = "Report the garbage arcave left in one session's store and in the Docker daemon; with dry_run " +
		"false, delete it. A deleted image's layers go on the next run; a removed savepoint's restic snapshots " +
		"are forgotten and pruned; a restored layer tag goes once no container uses it and it is older than " +
		"the grace. A busy project is refused: retry when its command finishes."
	descSessionUnset = "Clear the current session: every later tool call that is given no session works in " +
		"the default session (baseline) again, and so do the user's own arcave commands in this project " +
		"directory. It is also the way out when the current session no longer exists. Refused when the " +
		"server was started with --session."
)

// createInput is savepoint_create's input schema.
type createInput struct {
	Project     string `json:"project" jsonschema:"the docker compose project to operate on, as docker compose ls names it"`
	Session     string `json:"session,omitempty" jsonschema:"the session to work in for this call only, by name or id; omit for the current session"`
	Name        string `json:"name,omitempty" jsonschema:"a short name for the savepoint, used to refer to it later; ids are assigned by arcave"`
	Description string `json:"description,omitempty" jsonschema:"what this savepoint captures, e.g. the change it is being taken before"`
	WaitTimeout string `json:"wait_timeout,omitempty" jsonschema:"how long to wait for services to become healthy on restart, e.g. 2m or 90s; omit for the project's default"`
}

// createSavepoint snapshots every stateful mount in the project.
func (h *MCPHandler) createSavepointMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, in createInput) (*mcpsdk.CallToolResult, model.Savepoint, error) {
	waitTimeout, err := parseToolDuration("wait_timeout", in.WaitTimeout)
	if err != nil {
		return nil, model.Savepoint{}, err
	}
	svc, release, err := h.openService(ctx, in.Project, in.Session)
	if err != nil {
		return nil, model.Savepoint{}, err
	}
	defer release()

	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{
		Name:        in.Name,
		Description: in.Description,
		WaitTimeout: waitTimeout,
	})
	if err != nil {
		return nil, model.Savepoint{}, mcpError(composeProjectAbsentError(err, "the project argument"))
	}
	return nil, savepoint, nil
}

// parseToolDuration reads field's duration in the config's form ("2m", "90s"); an omitted
// field is zero, which the service reads as the project's default.
func parseToolDuration(field, s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s %q: want a unit-bearing value like 2m, 90s or 1h30m", field, s)
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
	Session                string `json:"session,omitempty" jsonschema:"the session to work in for this call only, by name or id; omit for the current session"`
	IncludeSafetySnapshots bool   `json:"include_safety_snapshots,omitempty" jsonschema:"also list the safety snapshots a rollback commits for its own undo; hidden by default"`
	Sort                   string `json:"sort,omitempty" jsonschema:"desc (newest first, the default) or asc"`
}

// listSavepoints reports the savepoints in the store. It reaches no daemon and
// takes no lock, so it is safe to call while another workflow is running.
func (h *MCPHandler) listSavepointsMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, in listSavepointInput) (*mcpsdk.CallToolResult, listOutput, error) {
	order, err := model.ParseSortOrder(in.Sort)
	if err != nil {
		return nil, listOutput{}, err
	}
	svc, release, err := h.openService(ctx, in.Project, in.Session)
	if err != nil {
		return nil, listOutput{}, err
	}
	defer release()

	savepoints, err := svc.ListSavepoints(ctx, model.ListRequest{IncludeSafetySnapshots: in.IncludeSafetySnapshots, Sort: order})
	if err != nil {
		return nil, listOutput{}, mcpError(err)
	}
	// A non-nil empty slice marshals as [] rather than null, so an agent reading
	// the result never has to tell "no savepoints" from "no such field".
	if savepoints == nil {
		savepoints = []model.Savepoint{}
	}
	return nil, listOutput{Savepoints: savepoints}, nil
}

// removeSavepointInput selects savepoints to remove, either by explicit IDs or by
// retention rules (the two modes are mutually exclusive).
type removeSavepointInput struct {
	Project      string   `json:"project" jsonschema:"the docker compose project to operate on, as docker compose ls names it"`
	Session      string   `json:"session,omitempty" jsonschema:"the session to work in for this call only, by name or id; omit for the current session"`
	IDs          []string `json:"ids,omitempty" jsonschema:"savepoint ids to delete; cannot be combined with the retention fields"`
	RetainRecent int      `json:"retain_recent,omitempty" jsonschema:"keep this many newest savepoints and delete the rest"`
	RetainFor    string   `json:"retain_for,omitempty" jsonschema:"keep savepoints younger than this duration, e.g. 24h or 7d, and delete the rest"`
	BeforeDate   string   `json:"before_date,omitempty" jsonschema:"delete savepoints created before this RFC 3339 timestamp, e.g. 2026-06-01T00:00:00Z"`
	DryRun       bool     `json:"dry_run,omitempty" jsonschema:"report what would be deleted without deleting anything"`
}

// removeSavepoints deletes the selected savepoints and reports what went.
func (h *MCPHandler) removeSavepointsMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, in removeSavepointInput) (*mcpsdk.CallToolResult, model.RemoveResponse, error) {
	svc, release, err := h.openService(ctx, in.Project, in.Session)
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
		return nil, model.RemoveResponse{}, mcpError(removeUsageError(err))
	}
	return nil, model.RemoveResponse(res), nil
}

// rollbackInput is rollback's input schema.
type rollbackInput struct {
	Project     string `json:"project" jsonschema:"the docker compose project to operate on, as docker compose ls names it"`
	Session     string `json:"session,omitempty" jsonschema:"the session to work in for this call only, by name or id; omit for the current session"`
	Ref         string `json:"ref" jsonschema:"the savepoint to restore: its id, its name, or the literal 'latest'"`
	WaitTimeout string `json:"wait_timeout,omitempty" jsonschema:"how long to wait for services to become healthy on restart, e.g. 2m or 90s; omit for the project's default"`
}

// rollback restores the environment to the referenced savepoint.
func (h *MCPHandler) rollbackMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, in rollbackInput) (*mcpsdk.CallToolResult, model.RollbackResponse, error) {
	waitTimeout, err := parseToolDuration("wait_timeout", in.WaitTimeout)
	if err != nil {
		return nil, model.RollbackResponse{}, err
	}
	svc, release, err := h.openService(ctx, in.Project, in.Session)
	if err != nil {
		return nil, model.RollbackResponse{}, err
	}
	defer release()

	res, err := svc.Rollback(ctx, newRollbackRequest(in.Ref, waitTimeout, h.leftoverVolumesPolicy))
	if err != nil {
		return nil, model.RollbackResponse{}, mcpError(err)
	}
	return nil, newRollbackResponse(res), nil
}

// composeGenerateInput is compose_generate's input schema.
type composeGenerateInput struct {
	Project       string `json:"project" jsonschema:"the docker compose project to generate the file of, as docker compose ls names it"`
	AbsolutePaths bool   `json:"absolute_paths,omitempty" jsonschema:"keep every bind source absolute instead of relative to the project's working directory"`
}

// composeGenerateOutput is the compose file and what it declares.
type composeGenerateOutput struct {
	ComposeProject model.DockerComposeProjectName `json:"compose_project" jsonschema:"the compose project the file describes"`
	Services       []string                       `json:"services" jsonschema:"the services the file declares, sorted"`
	Spec           string                         `json:"spec" jsonschema:"the compose file, as YAML"`
}

// generateComposeMCP returns the compose file of a running compose project.
func (h *MCPHandler) generateComposeMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, in composeGenerateInput) (*mcpsdk.CallToolResult, composeGenerateOutput, error) {
	svc, release, err := h.openToolsService(ctx, in.Project)
	if err != nil {
		return nil, composeGenerateOutput{}, err
	}
	defer release()

	generated, err := svc.GenerateCompose(ctx, model.GenerateComposeRequest{AbsolutePaths: in.AbsolutePaths})
	if err != nil {
		return nil, composeGenerateOutput{}, composeProjectAbsentError(err, "the project argument")
	}
	return nil, composeGenerateOutput{ComposeProject: generated.ComposeProject, Services: generated.Services, Spec: string(generated.Spec)}, nil
}

// gcInput is gc's input schema; dry_run left out is true, a grace left out keeps
// gc.restored_layer_grace, kinds every one.
type gcInput struct {
	DryRun             *bool                   `json:"dry_run,omitempty" jsonschema:"default true: report only; false deletes"`
	Session            string                  `json:"session,omitempty" jsonschema:"the session to look in, by name or id; default the pinned or current session"`
	RestoredLayerGrace string                  `json:"restored_layer_grace,omitempty" jsonschema:"how old an unused layer tag must be to count as garbage, as a Go duration such as 1h"`
	Kinds              []model.GarbageDataKind `json:"kinds,omitempty" jsonschema:"what to look at: savepoint-data, deduplicated-volumes, image-layers, upstream-images, restored-layers; default every one"`
}

// garbageCollectMCP finds the garbage in scope and, when dry_run is false, deletes it.
func (h *MCPHandler) garbageCollectMCP(
	ctx context.Context, _ *mcpsdk.CallToolRequest, in gcInput,
) (*mcpsdk.CallToolResult, model.GarbageCollectResult, error) {
	var grace *time.Duration
	if in.RestoredLayerGrace != "" {
		parsed, err := parseToolDuration("restored_layer_grace", in.RestoredLayerGrace)
		if err != nil {
			return nil, model.GarbageCollectResult{}, err
		}
		grace = &parsed
	}
	req := model.GarbageCollectRequest{
		DryRun:             in.DryRun == nil || *in.DryRun,
		DataKinds:          in.Kinds,
		RestoredLayerGrace: grace,
	}
	if err := req.Validate(); err != nil {
		return nil, model.GarbageCollectResult{}, err
	}
	svc, release, err := h.gcFactory(ctx, cmp.Or(in.Session, h.pinRef()))
	if err != nil {
		return nil, model.GarbageCollectResult{}, mcpError(err)
	}
	defer release()

	result, err := svc.GarbageCollect(ctx, req)
	var interrupted *model.GarbageCollectInterruptedError
	if errors.As(err, &interrupted) {
		// A gc stopped midway through deleting fails, but what it had deleted must still be seen.
		return &mcpsdk.CallToolResult{
			IsError: true,
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: mcpError(err).Error()}},
		}, result, nil
	}
	if err == nil || errors.Is(err, model.ErrGarbageCollectPartial) {
		// A partial gc is still a report: what could not be judged is in it, aborted or failed.
		return nil, result, nil
	}
	return nil, model.GarbageCollectResult{}, mcpError(err)
}

// sessionListInput is session_list's input schema.
type sessionListInput struct {
	All  bool   `json:"all,omitempty" jsonschema:"also list session trees without a record: forks cloning, failed or left incomplete"`
	Sort string `json:"sort,omitempty" jsonschema:"desc (newest first, the default) or asc"`
}

// sessionListOutput wraps the sessions, because MCP structured output must be a JSON object.
type sessionListOutput struct {
	Sessions []model.SessionSummary `json:"sessions" jsonschema:"the arcave project's sessions, newest first unless sort is asc"`
}

// listSessionsMCP reports the arcave project's sessions.
func (h *MCPHandler) listSessionsMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, in sessionListInput) (*mcpsdk.CallToolResult, sessionListOutput, error) {
	order, err := model.ParseSortOrder(in.Sort)
	if err != nil {
		return nil, sessionListOutput{}, err
	}
	svc, release, err := h.openSessionService(ctx)
	if err != nil {
		return nil, sessionListOutput{}, err
	}
	defer release()

	sessions, err := svc.ListSessions(ctx, model.ListSessionsRequest{All: in.All, Ref: h.pinRef(), Sort: order})
	if err != nil {
		return nil, sessionListOutput{}, mcpError(err)
	}
	if sessions == nil {
		sessions = []model.SessionSummary{}
	}
	return nil, sessionListOutput{Sessions: sessions}, nil
}

// sessionCreateInput is session_create's input schema.
type sessionCreateInput struct {
	Name string `json:"name,omitempty" jsonschema:"a name for the session, unique within the arcave project; ids are assigned by arcave"`
	From string `json:"from,omitempty" jsonschema:"the session to copy, by name or id; omit for an empty session"`
}

// createSessionMCP commits a session, empty or a fork of another.
func (h *MCPHandler) createSessionMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, in sessionCreateInput) (*mcpsdk.CallToolResult, model.Session, error) {
	svc, release, err := h.openSessionService(ctx)
	if err != nil {
		return nil, model.Session{}, err
	}
	defer release()

	session, err := svc.CreateSession(ctx, model.CreateSessionRequest{Name: in.Name, From: in.From})
	if err != nil {
		return nil, model.Session{}, mcpError(err)
	}
	return nil, session, nil
}

// sessionRemoveInput is session_remove's input schema.
type sessionRemoveInput struct {
	Session string `json:"session" jsonschema:"the session to remove, by name or id; a tree without a record by id only"`
	Force   bool   `json:"force,omitempty" jsonschema:"also remove the current session, resetting the current session to baseline, or a tree whose fork still looks alive"`
}

// removeSessionMCP removes a session and reports the bytes it freed.
func (h *MCPHandler) removeSessionMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, in sessionRemoveInput) (*mcpsdk.CallToolResult, model.RemoveSessionResponse, error) {
	svc, release, err := h.openSessionService(ctx)
	if err != nil {
		return nil, model.RemoveSessionResponse{}, err
	}
	defer release()

	res, err := svc.RemoveSession(ctx, model.RemoveSessionRequest{Session: in.Session, Force: in.Force, Pinned: h.pinnedSession})
	if err != nil {
		return nil, model.RemoveSessionResponse{}, mcpError(err)
	}
	return nil, res, nil
}

// sessionSetInput is session_set's input schema.
type sessionSetInput struct {
	Session string `json:"session" jsonschema:"the session to make current, by name or id; baseline is the default session"`
}

// setSessionMCP makes a session the current one; a server pinned by --session refuses.
func (h *MCPHandler) setSessionMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, in sessionSetInput) (*mcpsdk.CallToolResult, model.Session, error) {
	if err := h.requireUnpinned(); err != nil {
		return nil, model.Session{}, err
	}
	svc, release, err := h.openSessionService(ctx)
	if err != nil {
		return nil, model.Session{}, err
	}
	defer release()

	session, err := svc.SetSession(ctx, in.Session)
	if err != nil {
		return nil, model.Session{}, mcpError(err)
	}
	return nil, session, nil
}

// currentSessionMCP reports the session a call given no session works in.
func (h *MCPHandler) currentSessionMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, model.Session, error) {
	svc, release, err := h.openSessionService(ctx)
	if err != nil {
		return nil, model.Session{}, err
	}
	defer release()

	session, err := svc.ResolveSession(ctx, h.pinRef())
	if err != nil {
		return nil, model.Session{}, mcpError(err)
	}
	return nil, session, nil
}

// unsetSessionMCP clears the current session; a server pinned by --session refuses.
func (h *MCPHandler) unsetSessionMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, model.Session, error) {
	if err := h.requireUnpinned(); err != nil {
		return nil, model.Session{}, err
	}
	svc, release, err := h.openSessionService(ctx)
	if err != nil {
		return nil, model.Session{}, err
	}
	defer release()

	session, err := svc.UnsetSession(ctx)
	if err != nil {
		return nil, model.Session{}, mcpError(err)
	}
	return nil, session, nil
}

// requireUnpinned refuses to move the current session from a server pinned by --session, whose
// calls the pointer does not reach.
func (h *MCPHandler) requireUnpinned() error {
	if h.pinnedSession == (model.SessionID{}) {
		return nil
	}
	return fmt.Errorf("this server is pinned to session %q by --session or ARCAVE_SESSION, "+
		"which outranks the current session: pass session on each call, or restart the server without the pin", h.pinnedSession.String())
}

// pinRef is the pin as a session ref, "" when the server is not pinned.
func (h *MCPHandler) pinRef() string {
	if h.pinnedSession == (model.SessionID{}) {
		return ""
	}
	return h.pinnedSession.String()
}

// openSessionService builds the session commands for one tool call.
func (h *MCPHandler) openSessionService(ctx context.Context) (MCPSessionService, func(), error) {
	svc, release, err := h.sessionFactory(ctx)
	if err != nil {
		return nil, nil, mcpError(err)
	}
	return svc, release, nil
}

// openToolsService builds the tools commands for project, for one tool call.
func (h *MCPHandler) openToolsService(ctx context.Context, project string) (MCPToolsService, func(), error) {
	svc, release, err := h.toolsFactory(ctx, project)
	if err != nil {
		return nil, nil, mcpError(err)
	}
	return svc, release, nil
}

// mcpError names what holds the project or session lock, so an agent waits instead of retrying blind.
func mcpError(err error) error {
	switch {
	case errors.Is(err, model.ErrProjectLocked):
		return fmt.Errorf("%w: a session fork holds the lock for its whole copy, as does a savepoint or "+
			"rollback for its run; retry when it finishes", err)
	case errors.Is(err, model.ErrSessionLocked):
		return fmt.Errorf("%w: another arcave command, possibly on another machine sharing the store, works in "+
			"this session; retry when it finishes", err)
	}
	return err
}

// MCPRun serves the tools over stdio until the host closes stdin or ctx is cancelled. A
// cancelled ctx cancels no tool call: the server exits once the calls in flight have finished.
func (h *MCPHandler) MCPRun(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() {
		zerolog.Ctx(ctx).Info().Msg("MCP server interrupted: it exits once the tool calls in flight have finished")
	})
	defer stop()
	return h.Server().Run(ctx, &mcpsdk.StdioTransport{})
}

// Server is the MCP server with every tool registered, for any transport.
func (h *MCPHandler) Server() *mcpsdk.Server {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "arcave", Version: serverVersion}, nil)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "savepoint_create", Description: descCreate}, h.createSavepointMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "savepoint_list", Description: descList}, h.listSavepointsMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "savepoint_remove", Description: descRemove}, h.removeSavepointsMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "rollback", Description: descRollback}, h.rollbackMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "session_list", Description: descSessionList,
		OutputSchema: newOutputSchema[sessionListOutput]()}, h.listSessionsMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "session_create", Description: descSessionCreate,
		OutputSchema: newOutputSchema[model.Session]()}, h.createSessionMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "session_remove", Description: descSessionRemove,
		OutputSchema: newOutputSchema[model.RemoveSessionResponse]()}, h.removeSessionMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "session_current", Description: descSessionCurrent,
		OutputSchema: newOutputSchema[model.Session]()}, h.currentSessionMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "session_set", Description: descSessionSet,
		OutputSchema: newOutputSchema[model.Session]()}, h.setSessionMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "session_unset", Description: descSessionUnset,
		OutputSchema: newOutputSchema[model.Session]()}, h.unsetSessionMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "compose_generate", Description: descComposeGenerate}, h.generateComposeMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:         "gc",
		Description:  descGC,
		Annotations:  &mcpsdk.ToolAnnotations{DestructiveHint: utils.New(true)},
		OutputSchema: newOutputSchema[model.GarbageCollectResult](),
	}, h.garbageCollectMCP)

	return srv
}

// newOutputSchema infers T's schema with a session id as the string it marshals to: inferred from
// its Go type, it would be an array, and the SDK would refuse every result.
func newOutputSchema[T any]() *jsonschema.Schema {
	return utils.Must(jsonschema.For[T](&jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
		reflect.TypeFor[model.SessionID](): {Type: "string", Format: "uuid"},
	}}))
}
