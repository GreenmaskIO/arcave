package main

import (
	"github.com/spf13/cobra"

	ngcli "github.com/greenmaskio/arcave/internal/ng/cli"
)

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the sessions of the project",
	Args:  cobra.NoArgs,
	RunE:  runSessionList,
}

func runSessionList(cmd *cobra.Command, _ []string) error {
	if err := requireUsableConfig(); err != nil {
		return err
	}

	h := ngcli.NewSessionListHandler(newAPI(), newPrinter())
	return h.List(cmd.Context(), projectCfg, rootAddressing())
}

func init() {
	sessionCmd.AddCommand(sessionListCmd)
}
