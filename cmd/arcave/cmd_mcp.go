package main

import (
	"github.com/spf13/cobra"

	ngcli "github.com/greenmaskio/arcave/internal/ng/cli"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Serve arcave's savepoint workflows to an agent over MCP",
	Args:  cobra.NoArgs,
	RunE:  runMCP,
}

// runMCP passes no printer: stdout is the JSON-RPC channel.
func runMCP(cmd *cobra.Command, _ []string) error {
	if err := requireConfig(); err != nil {
		return err
	}

	h, err := ngcli.NewMCPHandler(newAPI(), projectCfg, rootAddressing())
	if err != nil {
		return err
	}
	return h.MCPRun(cmd.Context())
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}
