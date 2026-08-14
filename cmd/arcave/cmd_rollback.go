package main

import (
	"github.com/spf13/cobra"

	ngcli "github.com/greenmaskio/arcave/internal/ng/cli"
)

var (
	rollbackWithSafetySavepoint bool

	rollbackCmd = &cobra.Command{
		Use:   "rollback <savepoint-uuid>",
		Short: "Restore the stack to a specified savepoint",
		Args:  cobra.ExactArgs(1),
		RunE:  runRollback,
	}
)

func runRollback(cmd *cobra.Command, args []string) error {
	if err := requireConfig(); err != nil {
		return err
	}

	h := ngcli.NewRollbackHandler(newAPI(), newPrinter())
	return h.Rollback(cmd.Context(), projectCfg, rootAddressing(), ngcli.RollbackParams{
		SavepointID:         args[0],
		WithSafetySavepoint: rollbackWithSafetySavepoint,
	})
}

func init() {
	rollbackCmd.Flags().BoolVar(&rollbackWithSafetySavepoint, "with-safety-savepoint", true,
		"Capture the live state before restoring, so a failed rollback can be undone")

	rootCmd.AddCommand(rollbackCmd)
}
