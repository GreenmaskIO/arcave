package cmd

import "github.com/spf13/cobra"

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Display project information and storage usage",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
}
