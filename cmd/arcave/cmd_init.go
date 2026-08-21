package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/cli"
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

// runConfigInit creates the config the other commands require: it names no stack
// and opens neither the storage backend nor the Docker engine.
func runConfigInit(cmd *cobra.Command, _ []string) error {
	if configFound && !configInitOverride {
		return fmt.Errorf("project already initialized use --override")
	}

	svc, printer := initServiceDeps(projectRoot)
	h := cli.NewConfigInitHandler(svc, printer)
	return h.Init(cmd.Context(), projectCfg, configInitOverride)
}

func init() {
	flags := configInitCmd.Flags()
	flags.String("name", "",
		"Project name (default: current directory name)")
	flags.String("savepoint-dir", "",
		"Savepoint backup directory (default: .arcave/storage)")
	flags.BoolVar(&configInitOverride, "override", false,
		"Overwrite an existing arcave configuration")

	// --override is transient (never persisted), so it carries no config binding.
	// --savepoint-dir survives as sugar for the directory arm of the storage
	// union: it binds to storage.directory.path, not to a top-level key.
	viperBind(cfgViper, flags, map[string]string{
		"name":          "project.name",
		"savepoint-dir": "storage.directory.path",
	})

	rootCmd.AddCommand(configInitCmd)
}
