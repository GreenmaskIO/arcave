package main

import (
	"github.com/spf13/cobra"

	ngcli "github.com/greenmaskio/arcave/internal/ng/cli"
)

var (
	savepointListLimit                   int
	savepointListOffset                  int
	savepointListIncludeSafetySavepoints bool

	savepointListCmd = &cobra.Command{
		Use:   "list",
		Short: "List available savepoints",
		Args:  cobra.NoArgs,
		RunE:  runSavepointList,
	}
)

func runSavepointList(cmd *cobra.Command, _ []string) error {
	if err := requireConfig(); err != nil {
		return err
	}

	h := ngcli.NewSavepointListHandler(newAPI(), newPrinter())
	return h.List(cmd.Context(), projectCfg, rootAddressing(), ngcli.SavepointListParams{
		Limit:                   savepointListLimit,
		Offset:                  savepointListOffset,
		IncludeSafetySavepoints: savepointListIncludeSafetySavepoints,
	})
}

func init() {
	flags := savepointListCmd.Flags()
	flags.IntVar(&savepointListLimit, "limit", 0, "Maximum number of savepoints to return")
	flags.IntVar(&savepointListOffset, "offset", 0, "Number of savepoints to skip")
	flags.BoolVar(&savepointListIncludeSafetySavepoints, "include-safety-savepoints", false,
		"also list the safety savepoints a rollback takes of the live state; one that outlived its rollback can be rolled back to by id")

	savepointCmd.AddCommand(savepointListCmd)
}
