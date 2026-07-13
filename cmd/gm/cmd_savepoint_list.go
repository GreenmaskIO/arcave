package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/gm-tools/internal/cli"
)

var savepointListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available savepoints",
	Args:  cobra.NoArgs,
	RunE:  runSavepointList,
}

func runSavepointList(cmd *cobra.Command, _ []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	return withDeps(cmd, func(ctx context.Context, d *dependencies) error {
		h := cli.NewSavepointListHandler(d.service, d.printer)
		return h.SavepointList(ctx)
	})
}

func init() {
	savepointCmd.AddCommand(savepointListCmd)
}
