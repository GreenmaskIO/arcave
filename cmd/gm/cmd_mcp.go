package main

import (
	"errors"

	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Start the GM MCP server for AI agent integration",
	Args:  cobra.NoArgs,
	RunE: func(*cobra.Command, []string) error {
		return errors.New("not implemented")
	},
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}
