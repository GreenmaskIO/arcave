package main

import (
	"github.com/spf13/cobra"

	ngcli "github.com/greenmaskio/arcave/internal/ng/cli"
)

var projectListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the projects the control plane knows",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireUsableConfig(); err != nil {
			return err
		}
		return ngcli.NewProjectListHandler(newAPI(), newPrinter()).List(cmd.Context())
	},
}

func init() {
	projectCmd.AddCommand(projectListCmd)
}
