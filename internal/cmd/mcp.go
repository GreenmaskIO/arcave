package cmd

import "github.com/spf13/cobra"

func newMcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Start the GM MCP server for AI agent integration",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
}
