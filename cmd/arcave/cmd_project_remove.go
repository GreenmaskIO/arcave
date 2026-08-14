package main

import (
	"github.com/spf13/cobra"

	ngcli "github.com/greenmaskio/arcave/internal/ng/cli"
)

var projectRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a project from the control plane",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireUsableConfig(); err != nil {
			return err
		}
		return ngcli.NewProjectRemoveHandler(newAPI()).Remove(cmd.Context(), args[0])
	},
}

func init() {
	projectCmd.AddCommand(projectRemoveCmd)
}
