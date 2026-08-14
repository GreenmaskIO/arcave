package main

import "github.com/spf13/cobra"

var projectCmd = &cobra.Command{
	Use:   "project",
	Short: "Manage the project the control plane knows",
}

func init() {
	rootCmd.AddCommand(projectCmd)
}
