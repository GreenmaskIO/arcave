package cmd

import (
	"github.com/spf13/cobra"

	"github.com/greenmaskio/gm-tools/internal/adapter/cli"
)

func newInitCmd(h *cli.Handlers) *cobra.Command {
	var (
		name         string
		composeFiles []string
		savepointDir string
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize GM metadata for the current project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return h.Init(cmd, name, composeFiles, savepointDir)
		},
	}

	cmd.Flags().StringVar(&name, "name", "",
		"Project name (default: current directory name)")
	cmd.Flags().StringSliceVarP(&composeFiles, "file", "f", []string{"docker-compose.yaml"},
		"Docker Compose file (repeatable)")
	cmd.Flags().StringVar(&savepointDir, "savepoint-dir", "",
		"Savepoint backup directory (default: .gm/savepoints/<project_name>)")
	return cmd
}
