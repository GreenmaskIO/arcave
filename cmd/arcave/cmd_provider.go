package main

import "github.com/spf13/cobra"

var providerCmd = &cobra.Command{
	Use:   "provider",
	Short: "Inspect the providers registered with the control plane",
}

func init() {
	rootCmd.AddCommand(providerCmd)
}
