package cmd

import "github.com/spf13/cobra"

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "gm",
		Short: "Local environment lifecycle manager for stateful Docker Compose applications",
	}

	root.AddCommand(
		newDoctorCmd(),
		newInitCmd(),
		newSavepointCmd(),
		newRollbackCmd(),
		newStatusCmd(),
		newMcpCmd(),
	)

	return root
}
