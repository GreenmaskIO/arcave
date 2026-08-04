package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/cli"
	"github.com/greenmaskio/arcave/internal/model"
)

var (
	savepointListFlagIncludeSafetySnapshots = false

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
	return withDeps(cmd, func(ctx context.Context, d *dependencies) error {
		h := cli.NewSavepointListHandler(d.service, d.printer)
		return h.SavepointList(ctx, model.ListRequest{
			IncludeSafetySnapshots: savepointListFlagIncludeSafetySnapshots,
		})
	})
}

func init() {
	savepointListCmd.Flags().BoolVar(&savepointListFlagIncludeSafetySnapshots, "include-safety-snapshots", false,
		"also list the safety snapshots a rollback takes of the live state; one that outlived its rollback can be rolled back to by id")
	savepointCmd.AddCommand(savepointListCmd)
}
