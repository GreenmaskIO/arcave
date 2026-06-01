package cmd

import "github.com/spf13/cobra"

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Analyze the Docker Compose project for GM compatibility",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
}
