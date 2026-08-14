package main

import (
	"github.com/spf13/cobra"

	ngcli "github.com/greenmaskio/arcave/internal/ng/cli"
	cfgrepo "github.com/greenmaskio/arcave/internal/repository/config"
)

var (
	configInitOverride bool

	configInitCmd = &cobra.Command{
		Use:   "init",
		Short: "Initialize arcave metadata for the current project",
		Args:  cobra.NoArgs,
		RunE:  runConfigInit,
	}
)

// runConfigInit writes .arcave/config.yaml; `arcave project apply` is what sends it.
func runConfigInit(cmd *cobra.Command, _ []string) error {
	h := ngcli.NewInitHandler(cfgrepo.New(projectRoot))
	return h.Init(cmd.Context(), projectCfg, configInitOverride)
}

func init() {
	flags := configInitCmd.Flags()
	flags.String("name", "",
		"Project name (default: current directory name)")
	flags.BoolVar(&configInitOverride, "override", false,
		"Overwrite an existing arcave configuration")

	// --override is transient (never persisted), so it carries no config binding.
	viperBind(cfgViper, flags, map[string]string{
		"name": "project.name",
	})

	rootCmd.AddCommand(configInitCmd)
}
