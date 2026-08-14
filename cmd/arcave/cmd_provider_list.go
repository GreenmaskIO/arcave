package main

import (
	"github.com/spf13/cobra"

	ngcli "github.com/greenmaskio/arcave/internal/ng/cli"
)

var providerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the providers registered with the control plane",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireUsableConfig(); err != nil {
			return err
		}
		return ngcli.NewProviderListHandler(newAPI(), newPrinter()).List(cmd.Context())
	},
}

func init() {
	providerCmd.AddCommand(providerListCmd)
}
