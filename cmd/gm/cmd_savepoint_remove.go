package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/gm-tools/internal/cli"
	"github.com/greenmaskio/gm-tools/internal/model"
)

var (
	savepointRemoveRetainRecent int
	savepointRemoveRetainFor    string
	savepointRemoveBeforeDate   string
	savepointRemoveDryRun       bool

	savepointRemoveCmd = &cobra.Command{
		Use:   "remove [id...]",
		Short: "Remove savepoints by ID, or by retention rules",
		Args:  cobra.ArbitraryArgs,
		RunE:  runSavepointRemove,
	}
)

func runSavepointRemove(cmd *cobra.Command, args []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	return withDeps(cmd, func(ctx context.Context, d *dependencies) error {
		h := cli.NewSavepointRemoveHandler(d.service, d.printer)
		return h.SavepointRemove(ctx, model.RemoveRequest{
			IDs:          args,
			RetainRecent: savepointRemoveRetainRecent,
			RetainFor:    savepointRemoveRetainFor,
			BeforeDate:   savepointRemoveBeforeDate,
			DryRun:       savepointRemoveDryRun,
		})
	})
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
