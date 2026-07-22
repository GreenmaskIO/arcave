package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/gm-tools/internal/cli"
)

var (
	rollbackWaitTimeout int

	rollbackCmd = &cobra.Command{
		Use:   "rollback [savepoint-id|name|latest]",
		Short: "Restore the environment to a specified savepoint",
		Args:  cobra.ExactArgs(1),
		RunE:  runRollback,
	}
)

func runRollback(cmd *cobra.Command, args []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	return withDeps(cmd, func(ctx context.Context, d *dependencies) error {
		h := cli.NewRollbackHandler(d.service, d.printer)
		return h.Rollback(ctx, projectCfg, args[0], rollbackWaitTimeout)
	})
}

func init() {
	rollbackCmd.Flags().IntVar(&rollbackWaitTimeout, "wait-timeout", defaultUpWaitTimeout,
		"Seconds to wait for services to become healthy before reverting")

	rootCmd.AddCommand(rollbackCmd)
}
