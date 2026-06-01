package cmd

import "github.com/spf13/cobra"

func newSavepointCmd() *cobra.Command {
	sp := &cobra.Command{
		Use:   "savepoint",
		Short: "Manage environment savepoints",
	}
	sp.AddCommand(
		newSavepointCreateCmd(),
		newSavepointListCmd(),
		newSavepointRemoveCmd(),
	)
	return sp
}

func newSavepointCreateCmd() *cobra.Command {
	var name, description string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new environment savepoint",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Savepoint name")
	cmd.Flags().StringVar(&description, "description", "", "Savepoint description")
	return cmd
}

func newSavepointListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available savepoints",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
}

func newSavepointRemoveCmd() *cobra.Command {
	var (
		retainRecent int
		retainFor    string
		beforeDate   string
		dryRun       bool
	)

	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove savepoints",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
	cmd.Flags().IntVar(&retainRecent, "retain-recent", 0, "Retain the most recent N savepoints")
	cmd.Flags().StringVar(&retainFor, "retain-for", "", "Retain savepoints for the specified duration (e.g. 1w2d3h4m5s)")
	cmd.Flags().StringVar(&beforeDate, "before-date", "", "Delete savepoints older than RFC3339Nano timestamp")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show savepoints that would be deleted without deleting them")
	return cmd
}
