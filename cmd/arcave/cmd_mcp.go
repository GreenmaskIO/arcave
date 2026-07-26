package main

import (
	"context"

	"github.com/greenmaskio/arcave/internal/cli"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Start the arcave MCP server for AI agent integration",
	Args:  cobra.NoArgs,
	RunE:  runMCP,
}

func runMCP(cmd *cobra.Command, _ []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	return withDeps(cmd, func(ctx context.Context, d *dependencies) error {
		h := cli.NewMCPHandler(
			d.service,
			func(recommenderFiles []string) cli.RecommendService { return newRecommendService(recommenderFiles) },
			projectCfg.Project.ComposeFiles,
		)
		return h.MCPRun(ctx)
	})
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}
