package cmd

import (
	"github.com/spf13/cobra"

	"github.com/greenmaskio/gm-tools/internal/adapter/cli"
)

func newRollbackCmd(h *cli.Handlers) *cobra.Command {
	waitTimeout := cli.DefaultWaitTimeout

	cmd := &cobra.Command{
		Use:   "rollback [savepoint-id|name|latest]",
		Short: "Restore the environment to a specified savepoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return h.Rollback(cmd, args[0], waitTimeout)
		},
	}
	cmd.Flags().IntVar(&waitTimeout, "wait-timeout", cli.DefaultWaitTimeout,
		"Seconds to wait for services to become healthy before reverting")
	return cmd
}
