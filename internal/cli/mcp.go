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
	svc              mcpService
	recommendFactory RecommendServiceFactory
	composeFiles     []string
}

// New builds the MCP adapter over svc. The compose files are injected rather
// than taken per tool call: they are a property of the resolved project config,
// not something an agent should have to know or could keep in step with it. The
// adapter takes the file list rather than a config.Config because that is all it
// needs, and the whole config would drag internal/config into its import graph.
//
// recommendFactory is a factory rather than a second service for the reason
// stated on RecommendServiceFactory: the recommend tool takes its catalogs per
// call, and a service is bound to its catalog at construction.
func NewMCPHandler(svc mcpService, recommendFactory RecommendServiceFactory, composeFiles []string) *MCPHandler {
	return &MCPHandler{
		svc:              svc,
		recommendFactory: recommendFactory,
		composeFiles:     composeFiles,
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
	// descRecommend carries the workflow, not just the trigger: arcave exposes no
	// tool that applies a recommendation — editing the user's compose files is the
	// agent's job, done with its own editing tools after asking the user what to
	// apply and where. That decision lives here because this description is the
	// only place the agent reads it.
	//
	// The two questions are asked through the host's own structured-question tool
	// where there is one, because "apply which of these, and to which file" is
	// exactly a multi-select plus a single-select. It is named as a preference and
	// not a requirement: an MCP server does not get to know its host's tool
	// inventory, arcave is not Claude Code's alone, and that tool caps a question at
	// four options while a recommendation list has no bound. So the plain-text
	// fallback is spelled out too, and the numbered table above is what makes it
	// answerable — it is the fallback's option list.
	descRecommend = "Report volumes the project's services should mount but do not: a service whose " +
		"image is known to write persistent state to a path with no volume or bind mount on it, so " +
		"a savepoint would silently fail to capture that state. Strictly read-only — it reads the " +
		"compose files and the recommender catalog, and touches neither the Docker daemon, nor the " +
		"savepoint storage, nor the project lock. Returns compose_files (the files actually " +
		"checked) and recommendations, each with service, image, volume (the container path to " +
		"mount), operation ('AddVolume') and hash (stable id of the finding). An empty " +
		"recommendations array means there is nothing to advise.\n\n" +
		"arcave does NOT apply recommendations — you do. When the array is not empty: (1) render the " +
		"findings to the user as a markdown table, never as raw JSON, with one row per " +
		"recommendation and the columns '#' (a 1-based number the user can refer to), 'Service', " +
		"'Image' and 'Volume to add' (the container path); omit operation and hash, which are not " +
		"for the user to read; name the checked compose_files in a sentence under the table; " +
		"(2) ask the user two things — which recommendations to apply, and where to put them (one " +
		"of the files in compose_files, or a new override file docker-compose.arcave.yaml). If your " +
		"host offers a structured question tool (in Claude Code it is AskUserQuestion), use it and " +
		"ask both in a single call: the first question multi-select with one option per " +
		"recommendation labelled '<service> → <volume>', the second single-select with one option " +
		"per compose file plus 'new docker-compose.arcave.yaml'. That tool allows at most four " +
		"options per question, so when there are more than four recommendations — or the host has " +
		"no such tool — ask in plain text instead and let the user answer with the row numbers from " +
		"the table; (3) edit the chosen file yourself: add a named volume to " +
		"services.<service>.volumes as '<volume-name>:<volume-path>' and declare <volume-name> " +
		"under the top-level volumes: key. If you created a new file, add it to project.compose_files " +
		"in .arcave/config.yaml so savepoint and rollback see the new volumes, and tell the user " +
		"their own `docker compose` invocations must now pass it with -f."
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
		Restored:     res.Restored,
	}, nil
}

// recommendInput is recommend's input schema. Unlike every other tool here it
// does carry compose_files: recommend reads nothing but the files it is given, so
// an agent may legitimately check a file set the project config does not name —
// a compose file it is about to add, say. Omitting the field falls back to the
// configured set, which is what the other tools always use.
type recommendInput struct {
	ComposeFiles     []string `json:"compose_files,omitempty" jsonschema:"Docker Compose files to check; omit to use the project's configured compose files"`
	RecommenderFiles []string `json:"recommender_files,omitempty" jsonschema:"recommender catalog files to merge over the built-in catalog, in order; omit to use the built-in catalog alone"`
}

// recommendOutput wraps the recommendations, because MCP structured output must
// be a JSON object — the CLI prints the bare array Recommendations holds.
//
// ComposeFiles reports the files the check actually ran over, resolved fallback
// and all: the agent has to name them back to the user when it asks which file to
// add a volume to, and it cannot know the configured set otherwise.
type recommendOutput struct {
	ComposeFiles    []string               `json:"compose_files" jsonschema:"the compose files the check ran over"`
	Recommendations []model.Recommendation `json:"recommendations" jsonschema:"the volumes the project's services should mount but do not"`
}

// recommend reports the persistent volumes the project's services are missing. It
// reaches no daemon, opens no storage and takes no lock, so it is safe to call at
// any time — including while another workflow is running.
//
// The CLI turns "there is advice" into exit status 1; there is no equivalent here
// and none is wanted: a non-empty recommendations array says the same thing, and
// an MCP tool error means the check itself failed.
func (h *MCPHandler) recommendMCP(ctx context.Context, _ *mcpsdk.CallToolRequest, in recommendInput) (*mcpsdk.CallToolResult, recommendOutput, error) {
	files := in.ComposeFiles
	if len(files) == 0 {
		files = h.composeFiles
	}

	recs, err := h.recommendFactory(in.RecommenderFiles).Recommend(ctx, files...)
	if err != nil {
		return nil, recommendOutput{}, err
	}
	// A non-nil empty slice marshals as [] rather than null, so an agent reading
	// the result never has to tell "nothing to advise" from "no such field".
	if recs == nil {
		recs = []model.Recommendation{}
	}
	return nil, recommendOutput{ComposeFiles: files, Recommendations: recs}, nil
}

// Run registers the tools and serves them over stdio until the host closes stdin
// or ctx is cancelled.
func (h *MCPHandler) MCPRun(ctx context.Context) error {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "arcave", Version: serverVersion}, nil)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "savepoint_create", Description: descCreate}, h.createSavepointMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "savepoint_list", Description: descList}, h.listSavepointsMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "savepoint_remove", Description: descRemove}, h.removeSavepointsMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "rollback", Description: descRollback}, h.rollbackMCP)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "recommend", Description: descRecommend}, h.recommendMCP)

	return srv.Run(ctx, &mcpsdk.StdioTransport{})
}
