package main

import "github.com/spf13/cobra"

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage the sessions of a project",
}

func init() {
	rootCmd.AddCommand(sessionCmd)
}
