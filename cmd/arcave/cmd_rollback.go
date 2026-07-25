package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/cli"
)

var rollbackCmd = &cobra.Command{
	Use:   "rollback [savepoint-id|name|latest]",
	Short: "Restore the environment to a specified savepoint",
	Args:  cobra.ExactArgs(1),
	RunE:  runRollback,
}

func runRollback(cmd *cobra.Command, args []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	return withDeps(cmd, func(ctx context.Context, d *dependencies) error {
		h := cli.NewRollbackHandler(d.service, d.printer)
		return h.Rollback(ctx, projectCfg, args[0])
	})
}

func init() {
	// No flags of its own: the bound it waits for before reverting is
	// compose.wait_timeout, carried by rootCmd's persistent --wait-timeout.
	rootCmd.AddCommand(rollbackCmd)
}
