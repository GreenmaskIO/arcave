package cmd

import "github.com/spf13/cobra"

func newRollbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rollback [savepoint-id|latest]",
		Short: "Restore the environment to a specified savepoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
}
