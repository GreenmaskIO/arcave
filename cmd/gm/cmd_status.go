package main

import (
	"errors"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Display project information and storage usage",
	Args:  cobra.NoArgs,
	RunE: func(*cobra.Command, []string) error {
		return errors.New("not implemented")
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
