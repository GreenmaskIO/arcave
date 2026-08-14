// The MCP adapter serves arcave's workflows as tools. Transport is stdio, so
// nothing here may write to stdout.

package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	str2duration "github.com/xhit/go-str2duration/v2"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

// serverVersion is the version arcave reports to the MCP host during initialise.
const serverVersion = "0.1.0"

// mcpAPI is the control plane as the tools need it.
type mcpAPI interface {
	savepointCreateAPI
	savepointListAPI
	savepointRemoveAPI
	rollbackAPI
	recommendAPI
}

// MCPHandler serves arcave's savepoint workflows over MCP.
type MCPHandler struct {
	api   mcpAPI
	scope scope
}

// NewMCPHandler builds the MCP adapter over the control plane client, resolving
// the addressing once.
func NewMCPHandler(api mcpAPI, cfg config.Config, a Addressing) (*MCPHandler, error) {
	s, err := resolveScope(cfg, a)
	if err != nil {
		return nil, err
	}
	return &MCPHandler{api: api, scope: s}, nil
}

// The tool descriptions the MCP host shows an agent.
const (
	descCreate = "Create a savepoint: a consistent snapshot of every stateful volume of the " +
		"stack this server is bound to. Call this before a risky change (a schema migration, a " +
		"data transformation, a destructive experiment) so the environment can be restored " +
		"afterwards with the rollback tool. Services are stopped for the duration of the " +
		"snapshot and started again when it completes. The savepoint id it answers with is what " +
		"rollback and savepoint_remove take. If the call fails with a message of the form " +
		"'savepoint <id> committed; services failed to start', the savepoint EXISTS and is safe " +
		"to roll back to — only the restart failed, so report that id to the user and investigate " +
		"the stack (the services are stopped or unhealthy), rather than taking another savepoint."
	descList = "List the stack's savepoints with their id, name, description and creation time. " +
		"Read-only. Call it to find the savepoint id to pass to rollback or savepoint_remove. " +
		"Safety savepoints — the undo a rollback takes of the live state before it overwrites " +
		"it — are hidden unless include_safety_savepoints is set: one exists only while a " +
		"rollback runs, and one that outlived its rollback means that rollback was killed " +
		"partway. That leftover is the last copy of the pre-rollback state, so list it with the " +
		"flag and roll back to it by id to recover by hand."
	descRemove = "Delete savepoints and the snapshot data they hold. Select them either by " +
		"explicit ids, or by retention rules (retain_recent/retain_for/before_date) — the two " +
		"modes cannot be combined, and at least one is required. Pass dry_run to see what would " +
		"be deleted without deleting anything. Deletion is permanent."
	descRollback = "Restore the environment to a savepoint, named by its id. DESTRUCTIVE: every " +
		"volume it covers is wiped whole and replaced with the savepoint's data, so any change " +
		"made since that savepoint is lost — including in bind mounts, which are the user's own " +
		"host folders, and including the parts of a volume a service only sees a subpath of. " +
		"arcave takes a safety savepoint of the live state first and replays it automatically if " +
		"the rollback fails, so a failed rollback leaves the environment as it was. Services are " +
		"stopped and started again."
	descRecommend = "Report volumes the stack's services should mount but do not: a service whose " +
		"image is known to write persistent state to a path with no volume or bind mount on it, so " +
		"a savepoint would silently fail to capture that state. Strictly read-only — it stops " +
		"nothing and writes nothing. Returns one recommendation per finding, each with service, " +
		"image, volume (the container path to mount), operation ('AddVolume') and hash (stable id " +
		"of the finding). An empty recommendations array means there is nothing to advise.\n\n" +
		"arcave does NOT apply recommendations — you do. When the array is not empty: (1) render the " +
		"findings to the user as a markdown table, never as raw JSON, with one row per " +
		"recommendation and the columns '#' (a 1-based number the user can refer to), 'Service', " +
		"'Image' and 'Volume to add' (the container path); omit operation and hash, which are not " +
		"for the user to read; (2) ask the user which recommendations to apply and which compose " +
		"file to put them in. If your host offers a structured question tool (in Claude Code it is " +
		"AskUserQuestion), use it and ask both in a single call: the first question multi-select " +
		"with one option per recommendation labelled '<service> → <volume>', the second " +
		"single-select over the project's compose files plus 'new docker-compose.arcave.yaml'. " +
		"That tool allows at most four options per question, so when there are more than four " +
		"recommendations — or the host has no such tool — ask in plain text instead and let the " +
		"user answer with the row numbers from the table; (3) edit the chosen file yourself: add a " +
		"named volume to services.<service>.volumes as '<volume-name>:<volume-path>' and declare " +
		"<volume-name> under the top-level volumes: key."
)

// createInput is savepoint_create's input schema.
type createInput struct {
	Name        string            `json:"name,omitempty" jsonschema:"a short name for the savepoint, used to refer to it later; ids are assigned by arcave"`
	Description string            `json:"description,omitempty" jsonschema:"what this savepoint captures, e.g. the change it is being taken before"`
	Labels      map[string]string `json:"labels,omitempty" jsonschema:"free-form key/value marks carried with the savepoint"`
}

// createOutput is what savepoint_create answers with. Ids and timestamps are
// strings: the SDK reflects the schema, and uuid.UUID is a [16]byte.
type createOutput struct {
	SavepointID  string `json:"savepoint_id" jsonschema:"the id of the savepoint just created; pass it to rollback or savepoint_remove"`
	ProviderID   string `json:"provider_id" jsonschema:"the provider the stack lives on"`
	Stack        string `json:"stack" jsonschema:"the stack the savepoint covers"`
	ProviderType string `json:"provider_type" jsonschema:"what kind of provider took it"`
}

// savepointCreate snapshots every stateful mount of the stack.
func (h *MCPHandler) savepointCreate(ctx context.Context, _ *mcpsdk.CallToolRequest, in createInput) (*mcpsdk.CallToolResult, createOutput, error) {
	req := apimodel.SavepointCreateRequest{
		ProjectName: h.scope.ProjectName,
		SessionID:   h.scope.SessionID,
		ProviderID:  h.scope.ProviderID,
		StackID:     h.scope.StackID,
		Name:        in.Name,
		Description: in.Description,
		Labels:      in.Labels,
	}
	if err := req.Validate(); err != nil {
		return nil, createOutput{}, err
	}
	res, err := h.api.SavepointCreate(ctx, req)
	if err != nil {
		return nil, createOutput{}, err
	}
	return nil, createOutput{
		SavepointID:  res.SavepointID.String(),
		ProviderID:   res.ProviderID,
		Stack:        res.StackName,
		ProviderType: res.ProviderType,
	}, nil
}

// listInput is savepoint_list's input schema.
type listInput struct {
	Limit                   int  `json:"limit,omitempty" jsonschema:"maximum number of savepoints to return; omit for the server's own page size"`
	Offset                  int  `json:"offset,omitempty" jsonschema:"number of savepoints to skip, for paging through a long history"`
	IncludeSafetySavepoints bool `json:"include_safety_savepoints,omitempty" jsonschema:"also list the safety savepoints a rollback takes for its own undo; hidden by default"`
}

// savepointOut is one savepoint as an agent reads it.
type savepointOut struct {
	SavepointID string            `json:"savepoint_id" jsonschema:"the id to pass to rollback or savepoint_remove"`
	CreatedAt   string            `json:"created_at" jsonschema:"when it was taken, as an RFC3339 timestamp"`
	Name        string            `json:"name,omitempty" jsonschema:"the name it was given, if any"`
	Description string            `json:"description,omitempty" jsonschema:"what it captures"`
	Labels      map[string]string `json:"labels,omitempty" jsonschema:"the marks it carries"`
}

// listOutput is what savepoint_list answers with.
type listOutput struct {
	Stack       string         `json:"stack" jsonschema:"the stack the savepoints belong to"`
	ProviderID  string         `json:"provider_id" jsonschema:"the provider the stack lives on"`
	CurrentPage int            `json:"current_page" jsonschema:"the page this answer holds"`
	TotalPages  int            `json:"total_pages" jsonschema:"how many pages there are in total"`
	PageSize    int            `json:"page_size" jsonschema:"how many savepoints fit on a page"`
	Savepoints  []savepointOut `json:"savepoints" jsonschema:"the savepoints on this page; empty when there are none"`
}

// savepointList reports the stack's savepoints.
func (h *MCPHandler) savepointList(ctx context.Context, _ *mcpsdk.CallToolRequest, in listInput) (*mcpsdk.CallToolResult, listOutput, error) {
	req := apimodel.SavepointListRequest{
		ProjectName:             h.scope.ProjectName,
		ProviderID:              h.scope.ProviderID,
		StackID:                 h.scope.StackID,
		Limit:                   in.Limit,
		Offset:                  in.Offset,
		IncludeSafetySavepoints: in.IncludeSafetySavepoints,
	}
	if err := req.Validate(); err != nil {
		return nil, listOutput{}, err
	}
	res, err := h.api.SavepointList(ctx, req)
	if err != nil {
		return nil, listOutput{}, err
	}

	// A non-nil empty slice marshals as [] rather than null.
	savepoints := make([]savepointOut, 0, len(res.Records))
	for _, r := range res.Records {
		savepoints = append(savepoints, savepointOut{
			SavepointID: r.SavepointID.String(),
			CreatedAt:   r.CreatedAt.Format(time.RFC3339),
			Name:        r.Name,
			Description: r.Description,
			Labels:      r.Labels,
		})
	}
	return nil, listOutput{
		Stack:       res.StackName,
		ProviderID:  res.ProviderID,
		CurrentPage: res.CurrentPage,
		TotalPages:  res.TotalPages,
		PageSize:    res.PageSize,
		Savepoints:  savepoints,
	}, nil
}

// removeInput selects savepoints to remove, by explicit ids or by retention
// rules; the two modes are mutually exclusive.
type removeInput struct {
	IDs          []string `json:"ids,omitempty" jsonschema:"savepoint ids to delete; cannot be combined with the retention fields"`
	RetainRecent *int     `json:"retain_recent,omitempty" jsonschema:"keep this many newest savepoints and delete the rest"`
	RetainFor    string   `json:"retain_for,omitempty" jsonschema:"keep savepoints younger than this duration, e.g. 24h or 7d, and delete the rest"`
	BeforeDate   string   `json:"before_date,omitempty" jsonschema:"delete savepoints created before this date (YYYY-MM-DD, or a full RFC3339 timestamp)"`
	DryRun       bool     `json:"dry_run,omitempty" jsonschema:"report what would be deleted without deleting anything"`
}

// removeOutput is what savepoint_remove answers with.
type removeOutput struct {
	Removed bool `json:"removed" jsonschema:"true when the request was accepted; with dry_run nothing was deleted"`
	DryRun  bool `json:"dry_run" jsonschema:"whether the call only reported what would be deleted"`
}

// savepointRemove deletes the selected savepoints.
func (h *MCPHandler) savepointRemove(ctx context.Context, _ *mcpsdk.CallToolRequest, in removeInput) (*mcpsdk.CallToolResult, removeOutput, error) {
	req, err := h.removeRequest(in)
	if err != nil {
		return nil, removeOutput{}, err
	}
	if err := req.Validate(); err != nil {
		return nil, removeOutput{}, removeUsageError(err)
	}
	if err := h.api.SavepointRemove(ctx, req); err != nil {
		return nil, removeOutput{}, err
	}
	return nil, removeOutput{Removed: !in.DryRun, DryRun: in.DryRun}, nil
}

// removeRequest parses the selectors the tool call carries as text.
func (h *MCPHandler) removeRequest(in removeInput) (apimodel.SavepointRemoveRequest, error) {
	req := apimodel.SavepointRemoveRequest{
		ProjectName:  h.scope.ProjectName,
		SessionID:    h.scope.SessionID,
		ProviderID:   h.scope.ProviderID,
		StackID:      h.scope.StackID,
		RetainRecent: in.RetainRecent,
		DryRun:       in.DryRun,
	}
	for _, raw := range in.IDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			return apimodel.SavepointRemoveRequest{}, fmt.Errorf("savepoint id %q: want a uuid: %w", raw, err)
		}
		req.SavepointIDs = append(req.SavepointIDs, id)
	}
	if in.RetainFor != "" {
		d, err := str2duration.ParseDuration(in.RetainFor)
		if err != nil {
			return apimodel.SavepointRemoveRequest{}, fmt.Errorf("retain_for %q: want a unit-bearing value like 24h or 7d", in.RetainFor)
		}
		req.RetainFor = &d
	}
	if in.BeforeDate != "" {
		ts, err := parseBeforeDate(in.BeforeDate)
		if err != nil {
			return apimodel.SavepointRemoveRequest{}, err
		}
		req.BeforeDate = &ts
	}
	return req, nil
}

// parseBeforeDate accepts a plain date as well as a full RFC3339 timestamp.
func parseBeforeDate(s string) (time.Time, error) {
	if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return ts, nil
	}
	ts, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("before_date %q: want YYYY-MM-DD or an RFC3339 timestamp", s)
	}
	return ts, nil
}

// removeUsageError rephrases a selector sentinel in terms of the tool's fields.
func removeUsageError(err error) error {
	switch {
	case errors.Is(err, apimodel.ErrRemoveSelectorConflict):
		return fmt.Errorf("%w: pass either ids, or retain_recent/retain_for/before_date", err)
	case errors.Is(err, apimodel.ErrRemoveNoSelector):
		return fmt.Errorf("%w: pass ids, or one of retain_recent/retain_for/before_date", err)
	case errors.Is(err, apimodel.ErrRetainRecentNegative):
		return fmt.Errorf("%w: retain_recent must be zero or positive", err)
	default:
		return err
	}
}

// rollbackInput is rollback's input schema.
type rollbackInput struct {
	SavepointID         string `json:"savepoint_id" jsonschema:"the id of the savepoint to restore"`
	WithSafetySavepoint *bool  `json:"with_safety_savepoint,omitempty" jsonschema:"capture the live state first so a failed rollback can be undone; on by default"`
}

// itemOut is what became of one thing the rollback worked on.
type itemOut struct {
	Name   string            `json:"name" jsonschema:"what was acted on"`
	Kind   string            `json:"kind" jsonschema:"what kind of thing it is"`
	Status string            `json:"status" jsonschema:"what became of it"`
	Error  string            `json:"error,omitempty" jsonschema:"why it failed, when it did"`
	Meta   map[string]string `json:"meta,omitempty" jsonschema:"whatever else the provider reported"`
}

// rollbackOutput is what rollback answers with.
type rollbackOutput struct {
	Status            string    `json:"status" jsonschema:"success, warning or failure"`
	StartedAt         string    `json:"started_at" jsonschema:"when the rollback began, as an RFC3339 timestamp"`
	CompletedAt       string    `json:"completed_at" jsonschema:"when it ended, as an RFC3339 timestamp"`
	SafetySavepointID string    `json:"safety_savepoint_id,omitempty" jsonschema:"the savepoint taken of the live state before the restore; roll back to it to undo this rollback"`
	RolledBack        []itemOut `json:"rolled_back" jsonschema:"what the restore did, item by item"`
	SafetySavepoint   []itemOut `json:"safety_savepoint" jsonschema:"what the safety capture did, item by item"`
}

// rollback restores the environment to the named savepoint.
func (h *MCPHandler) rollback(ctx context.Context, _ *mcpsdk.CallToolRequest, in rollbackInput) (*mcpsdk.CallToolResult, rollbackOutput, error) {
	savepointID, err := uuid.Parse(in.SavepointID)
	if err != nil {
		return nil, rollbackOutput{}, fmt.Errorf("savepoint_id %q: want a uuid: %w", in.SavepointID, err)
	}
	req := apimodel.SavepointRollbackRequest{
		ProjectName:         h.scope.ProjectName,
		SessionID:           h.scope.SessionID,
		ProviderID:          h.scope.ProviderID,
		StackID:             h.scope.StackID,
		SavepointID:         savepointID,
		WithSafetySavepoint: in.WithSafetySavepoint == nil || *in.WithSafetySavepoint,
	}
	if err := req.Validate(); err != nil {
		return nil, rollbackOutput{}, err
	}
	res, err := h.api.SavepointRollback(ctx, req)
	if err != nil {
		return nil, rollbackOutput{}, err
	}

	out := rollbackOutput{
		Status:          string(res.Status),
		StartedAt:       res.StartedAt.Format(time.RFC3339),
		CompletedAt:     res.CompletedAt.Format(time.RFC3339),
		RolledBack:      itemsOut(res.RollbackItemStatues),
		SafetySavepoint: itemsOut(res.SafetySavepointItemStatues),
	}
	if res.SafetySavepointID != uuid.Nil {
		out.SafetySavepointID = res.SafetySavepointID.String()
	}
	return nil, out, nil
}

// itemsOut renders a run's per-item statuses, reporting an unnameable identity
// by kind alone.
func itemsOut(items []apimodel.ItemStatus) []itemOut {
	out := make([]itemOut, 0, len(items))
	for _, it := range items {
		name, err := it.Identity.Name()
		if err != nil {
			name = ""
		}
		item := itemOut{Name: name, Kind: it.Identity.Kind, Status: it.Status, Meta: it.Meta}
		if it.Error != nil && *it.Error != nil {
			item.Error = (*it.Error).Error()
		}
		out = append(out, item)
	}
	return out
}

// recommendInput is recommend's input schema: the tool takes nothing.
type recommendInput struct{}

// recommendOutput wraps the findings: MCP structured output must be an object.
type recommendOutput struct {
	Stack           string                 `json:"stack" jsonschema:"the stack that was checked"`
	Recommendations []model.Recommendation `json:"recommendations" jsonschema:"the volumes the stack's services should mount but do not; empty when there is nothing to advise"`
}

// recommend reports the persistent volumes the stack's services are missing.
func (h *MCPHandler) recommend(ctx context.Context, _ *mcpsdk.CallToolRequest, _ recommendInput) (*mcpsdk.CallToolResult, recommendOutput, error) {
	req := apimodel.RecommendRequest{
		ProjectName: h.scope.ProjectName,
		ProviderID:  h.scope.ProviderID,
		StackID:     h.scope.StackID,
	}
	if err := req.Validate(); err != nil {
		return nil, recommendOutput{}, err
	}
	res, err := h.api.Recommend(ctx, req)
	if err != nil {
		return nil, recommendOutput{}, err
	}

	recs := res.Recommendations
	if recs == nil {
		recs = []model.Recommendation{}
	}
	return nil, recommendOutput{Stack: res.StackName, Recommendations: recs}, nil
}

// MCPRun registers the tools and serves them over stdio until the host closes
// stdin or ctx is cancelled.
func (h *MCPHandler) MCPRun(ctx context.Context) error {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "arcave", Version: serverVersion}, nil)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "savepoint_create", Description: descCreate}, h.savepointCreate)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "savepoint_list", Description: descList}, h.savepointList)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "savepoint_remove", Description: descRemove}, h.savepointRemove)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "rollback", Description: descRollback}, h.rollback)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "recommend", Description: descRecommend}, h.recommend)

	return srv.Run(ctx, &mcpsdk.StdioTransport{})
}
