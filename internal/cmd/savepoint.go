package cmd

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/gm-tools/internal/adapter/cli"
)

func newSavepointCmd(h *cli.Handlers) *cobra.Command {
	sp := &cobra.Command{
		Use:   "savepoint",
		Short: "Manage environment savepoints",
	}
	sp.AddCommand(
		newSavepointCreateCmd(h),
		newSavepointListCmd(h),
		newSavepointRemoveCmd(h),
	)
	return sp
}

func newSavepointCreateCmd(h *cli.Handlers) *cobra.Command {
	var name, description string

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new environment savepoint",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if name != "" && name != args[0] {
					return errors.New("savepoint name given both as argument and --name")
				}
				name = args[0]
			}
			return h.SavepointCreate(cmd, name, description)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Savepoint name")
	cmd.Flags().StringVar(&description, "description", "", "Savepoint description")
	return cmd
}

func newSavepointListCmd(h *cli.Handlers) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available savepoints",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return h.SavepointList(cmd)
		},
	}
}

func newSavepointRemoveCmd(h *cli.Handlers) *cobra.Command {
	var (
		retainRecent int
		retainFor    string
		beforeDate   string
		dryRun       bool
	)

	cmd := &cobra.Command{
		Use:   "remove [id...]",
		Short: "Remove savepoints by ID, or by retention rules",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return h.SavepointRemove(cmd, args, retainRecent, retainFor, beforeDate, dryRun)
		},
	}
	cmd.Flags().IntVar(&retainRecent, "retain-recent", 0, "Retain the most recent N savepoints")
	cmd.Flags().StringVar(&retainFor, "retain-for", "", "Retain savepoints for the specified duration (e.g. 1w2d3h4m5s)")
	cmd.Flags().StringVar(&beforeDate, "before-date", "", "Delete savepoints older than RFC3339Nano timestamp")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show savepoints that would be deleted without deleting them")
	return cmd
}
