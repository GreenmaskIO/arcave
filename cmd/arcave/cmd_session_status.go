package main

import (
	"github.com/spf13/cobra"

	ngcli "github.com/greenmaskio/arcave/internal/ng/cli"
)

var sessionStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report the status of the current session",
	Args:  cobra.NoArgs,
	RunE:  runSessionStatus,
}

func runSessionStatus(cmd *cobra.Command, _ []string) error {
	if err := requireUsableConfig(); err != nil {
		return err
	}

	h := ngcli.NewSessionStatusHandler(newAPI(), newPrinter())
	return h.Status(cmd.Context(), projectCfg, rootAddressing())
}

func init() {
	sessionCmd.AddCommand(sessionStatusCmd)
}
