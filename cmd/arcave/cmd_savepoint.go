package main

import "github.com/spf13/cobra"

// savepointCmd groups the savepoint subcommands; it does no work of its own.
var savepointCmd = &cobra.Command{
	Use:   "savepoint",
	Short: "Manage environment savepoints",
}

func init() {
	rootCmd.AddCommand(savepointCmd)
}
