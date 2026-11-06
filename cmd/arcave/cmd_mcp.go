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

package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/cli"
	"github.com/greenmaskio/arcave/internal/model"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Start the arcave MCP server for AI agent integration",
	Args:  cobra.NoArgs,
	RunE:  runMCP,
}

// runMCP builds no service of its own: one server serves many compose projects and
// sessions, both named per tool call.
func runMCP(cmd *cobra.Command, _ []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	// --session or ARCAVE_SESSION pins the server to the session it names now, by id: a session
	// created later under the same name is not the pinned one.
	var pinnedSession model.SessionID
	if sessionRef != "" {
		var err error
		if pinnedSession, err = resolveSession(cmd.Context(), sessionRef); err != nil {
			return err
		}
	}
	return cli.NewMCPHandler(newMCPFactories(), pinnedSession, projectCfg.Rollback.Policy()).MCPRun(cmd.Context())
}

// newMCPFactories is what every family of tools is built by, for one tool call each.
func newMCPFactories() cli.MCPFactories {
	return cli.MCPFactories{
		Savepoint:      mcpSavepointServiceFactory,
		SavepointStore: mcpSavepointStoreServiceFactory,
		Session:        mcpSessionServiceFactory,
		Tools:          mcpToolsServiceFactory,
		GC:             mcpGCServiceFactory,
	}
}

// mcpSavepointServiceFactory wires the savepoint workflows for a tool call's compose project and
// session ("" is the current one), the daemon connected, plus the release closing them.
func mcpSavepointServiceFactory(
	ctx context.Context, project, sessionRef string,
) (cli.MCPSavepointService, func(), error) {
	var opened closers
	svc, err := openSavepointService(ctx, projectRoot, projectCfg, sessionRef, model.DockerComposeProjectName(project), &opened)
	if err != nil {
		opened.release(ctx)
		return nil, nil, fmt.Errorf("init dependencies: %w", err)
	}
	return svc, func() { opened.release(ctx) }, nil
}

// mcpSavepointStoreServiceFactory is mcpSavepointServiceFactory for a tool that reads or deletes
// in the store alone: nothing that reaches the daemon is built.
func mcpSavepointStoreServiceFactory(
	ctx context.Context, project, sessionRef string,
) (cli.MCPSavepointStoreService, func(), error) {
	var opened closers
	svc, err := openSavepointStoreService(ctx, projectRoot, projectCfg, sessionRef, model.DockerComposeProjectName(project), &opened)
	if err != nil {
		opened.release(ctx)
		return nil, nil, fmt.Errorf("init dependencies: %w", err)
	}
	return svc, func() { opened.release(ctx) }, nil
}

// mcpSessionServiceFactory wires the session commands for one tool call.
func mcpSessionServiceFactory(ctx context.Context) (cli.MCPSessionService, func(), error) {
	var opened closers
	svc, err := openSessionService(ctx, projectRoot, projectCfg, &opened)
	if err != nil {
		opened.release(ctx)
		return nil, nil, fmt.Errorf("init session dependencies: %w", err)
	}
	return svc, func() { opened.release(ctx) }, nil
}

// mcpToolsServiceFactory wires the tools commands for the compose project a tool call named.
func mcpToolsServiceFactory(ctx context.Context, project string) (cli.MCPToolsService, func(), error) {
	var opened closers
	svc, err := openComposeFileService(ctx, projectCfg, model.DockerComposeProjectName(project), &opened)
	if err != nil {
		opened.release(ctx)
		return nil, nil, fmt.Errorf("init tools dependencies: %w", err)
	}
	return svc, func() { opened.release(ctx) }, nil
}

// mcpGCServiceFactory wires the gc for the session a tool call named.
func mcpGCServiceFactory(ctx context.Context, sessionRef string) (cli.MCPGCService, func(), error) {
	var opened closers
	svc, err := openGCService(ctx, projectRoot, projectCfg, sessionRef, &opened)
	if err != nil {
		opened.release(ctx)
		return nil, nil, fmt.Errorf("init gc dependencies: %w", err)
	}
	return svc, func() { opened.release(ctx) }, nil
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}
