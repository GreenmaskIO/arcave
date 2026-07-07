package cmd

import (
	"github.com/spf13/cobra"

	"github.com/greenmaskio/gm-tools/internal/adapter/cli"
	"github.com/greenmaskio/gm-tools/internal/appconfig"
	"github.com/greenmaskio/gm-tools/internal/utils"
)

func newInitCmd(h *cli.Handlers, res *appconfig.Resolver) *cobra.Command {
	var override bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize GM metadata for the current project",
		Args:  cobra.NoArgs,
		// init runs before the config file exists, so it resolves the config from
		// flags and defaults (found is expected to be false) and sets up the
		// logger itself rather than using the shared resolveConfig helper.
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := res.Resolve()
			if err != nil {
				return err
			}
			if err := utils.SetDefaultContextLogger(cfg.Log.Level, cfg.Log.Format); err != nil {
				return err
			}
			return h.Init(cmd.Context(), cfg, override)
		},
	}

	cmd.Flags().String("name", "",
		"Project name (default: current directory name)")
	cmd.Flags().StringSliceP("file", "f", []string{"docker-compose.yaml"},
		"Docker Compose file (repeatable)")
	cmd.Flags().String("savepoint-dir", "",
		"Savepoint backup directory (default: .gm/savepoints/<project_name>)")
	cmd.Flags().BoolVar(&override, "override", false,
		"Overwrite an existing GM configuration")

	res.Bind(cmd.Flags(), "name", "name")
	res.Bind(cmd.Flags(), "file", "compose_files")
	res.Bind(cmd.Flags(), "savepoint-dir", "savepoint_dir")
	// --override is transient (not persisted), so it carries no config binding.
	return cmd
}
