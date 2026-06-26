// Package cmd defines GM's Cobra command tree: flags, usage, and short help.
// Each command's RunE delegates to the CLI adapter, which calls the services.
package cmd

import (
	"github.com/spf13/cobra"

	"github.com/greenmaskio/gm-tools/internal/adapter/cli"
)

// NewRootCmd builds the gm command tree wired to the given CLI handlers.
func NewRootCmd(h *cli.Handlers) *cobra.Command {
	root := &cobra.Command{
		Use:   "gm",
		Short: "Local environment lifecycle manager for stateful Docker Compose applications",
	}

	root.AddCommand(
		newInitCmd(h),
		newSavepointCmd(h),
		newRollbackCmd(h),
		newStatusCmd(),
		newMcpCmd(),
	)

	return root
}
