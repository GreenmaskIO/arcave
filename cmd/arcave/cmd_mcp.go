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

	"github.com/rs/zerolog/log"
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

// runMCP builds no dependencies of its own: one server serves many compose projects and
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
	return cli.NewMCPHandler(mcpServiceFactory, mcpSessionServiceFactory, mcpToolsServiceFactory, mcpGCServiceFactory,
		pinnedSession, projectCfg.Rollback.Policy()).MCPRun(cmd.Context())
}

// mcpServiceFactory wires the savepoint workflows for the compose project and session a tool
// call named, "" being the current session, and returns the release that closes them.
func mcpServiceFactory(ctx context.Context, project, sessionRef string) (cli.MCPService, func(), error) {
	deps, err := newDependencies(ctx, projectRoot, projectCfg, sessionRef, model.DockerComposeProjectName(project))
	if err != nil {
		return nil, nil, fmt.Errorf("init dependencies: %w", err)
	}
	return deps.service, func() {
		if err := deps.Close(ctx); err != nil {
			log.Ctx(ctx).Error().Err(err).Msg("close all dependencies")
		}
	}, nil
}

// mcpSessionServiceFactory wires the session commands for one tool call.
func mcpSessionServiceFactory(ctx context.Context) (cli.MCPSessionService, func(), error) {
	deps, err := newSessionDependencies(ctx, projectRoot, projectCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("init session dependencies: %w", err)
	}
	return deps.service, func() {
		if err := deps.Close(); err != nil {
			log.Ctx(ctx).Error().Err(err).Msg("close session dependencies")
		}
	}, nil
}

// mcpToolsServiceFactory wires the tools commands for the compose project a tool call named.
func mcpToolsServiceFactory(ctx context.Context, project string) (cli.MCPToolsService, func(), error) {
	deps, err := newToolsDependencies(projectCfg, model.DockerComposeProjectName(project))
	if err != nil {
		return nil, nil, fmt.Errorf("init tools dependencies: %w", err)
	}
	return deps.service, func() {
		if err := deps.Close(); err != nil {
			log.Ctx(ctx).Error().Err(err).Msg("close tools dependencies")
		}
	}, nil
}

// mcpGCServiceFactory wires the gc for the session a tool call named.
func mcpGCServiceFactory(ctx context.Context, sessionRef string) (cli.MCPGCService, func(), error) {
	deps, err := newGCDependencies(ctx, projectRoot, projectCfg, sessionRef)
	if err != nil {
		return nil, nil, fmt.Errorf("init gc dependencies: %w", err)
	}
	return deps.service, func() {
		if err := deps.Close(ctx); err != nil {
			log.Ctx(ctx).Error().Err(err).Msg("close gc dependencies")
		}
	}, nil
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}
