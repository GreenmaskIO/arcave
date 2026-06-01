package cmd

import "github.com/spf13/cobra"

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize GM metadata for the current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
}
