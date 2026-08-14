package main

import (
	"github.com/spf13/cobra"

	ngcli "github.com/greenmaskio/arcave/internal/ng/cli"
)

var (
	savepointRemoveRetainRecent int
	savepointRemoveRetainFor    string
	savepointRemoveBeforeDate   string
	savepointRemoveDryRun       bool

	savepointRemoveCmd = &cobra.Command{
		Use:   "remove [uuid...]",
		Short: "Remove savepoints by id, or by retention rules",
		Args:  cobra.ArbitraryArgs,
		RunE:  runSavepointRemove,
	}
)

func runSavepointRemove(cmd *cobra.Command, args []string) error {
	if err := requireConfig(); err != nil {
		return err
	}

	params := ngcli.SavepointRemoveParams{
		IDs:        args,
		RetainFor:  savepointRemoveRetainFor,
		BeforeDate: savepointRemoveBeforeDate,
		DryRun:     savepointRemoveDryRun,
	}
	// Zero is a retention count the user may mean, so only a changed flag fills it.
	if cmd.Flags().Changed("retain-recent") {
		params.RetainRecent = &savepointRemoveRetainRecent
	}

	h := ngcli.NewSavepointRemoveHandler(newAPI())
	return h.Remove(cmd.Context(), projectCfg, rootAddressing(), params)
}

func init() {
	flags := savepointRemoveCmd.Flags()
	flags.IntVar(&savepointRemoveRetainRecent, "retain-recent", 0,
		"Retain the most recent N savepoints")
	flags.StringVar(&savepointRemoveRetainFor, "retain-for", "",
		"Retain savepoints for the specified duration (e.g. 1w2d3h4m5s)")
	flags.StringVar(&savepointRemoveBeforeDate, "before-date", "",
		"Delete savepoints older than RFC3339Nano timestamp")
	flags.BoolVar(&savepointRemoveDryRun, "dry-run", false,
		"Show savepoints that would be deleted without deleting them")

	savepointCmd.AddCommand(savepointRemoveCmd)
}
