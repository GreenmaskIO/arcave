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

// runMCP builds no dependencies of its own: one server serves many stacks, and a
// stack is named per tool call.
func runMCP(cmd *cobra.Command, _ []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	return cli.NewMCPHandler(mcpServiceFactory).MCPRun(cmd.Context())
}

// mcpServiceFactory wires the savepoint workflows for the project a tool call
// named, and returns the release that closes them when that call returns.
func mcpServiceFactory(ctx context.Context, project string) (cli.MCPService, func(), error) {
	deps, err := initAllDependencies(ctx, projectRoot, projectCfg, model.StackName(project))
	if err != nil {
		return nil, nil, fmt.Errorf("init dependencies: %w", err)
	}
	return deps.service, func() {
		if err := deps.Close(ctx); err != nil {
			log.Ctx(ctx).Error().Err(err).Msg("close all dependencies")
		}
	}, nil
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}
