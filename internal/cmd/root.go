// Package cmd defines GM's Cobra command tree: flags, usage, and short help.
// Each command binds its flags to the shared appconfig.Resolver where they are
// defined; its RunE resolves the effective config, sets up the logger, and
// delegates to the CLI adapter, which calls the services.
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/gm-tools/internal/adapter/cli"
	"github.com/greenmaskio/gm-tools/internal/appconfig"
	"github.com/greenmaskio/gm-tools/internal/config"
	"github.com/greenmaskio/gm-tools/internal/utils"
)

// NewRootCmd builds the gm command tree wired to the given CLI handlers. root is
// the project root; it seeds the config resolver shared by every command.
func NewRootCmd(h *cli.Handlers, root string) *cobra.Command {
	res := appconfig.NewResolver(root)

	rootCmd := &cobra.Command{
		Use:   "gm",
		Short: "Local environment lifecycle manager for stateful Docker Compose applications",
	}

	// Persistent log flags are available to every command; binding them here
	// feeds the config (flag > env > file > default) that drives the logger set
	// up in each RunE. Logs go to stderr; stdout stays reserved for JSON output.
	pf := rootCmd.PersistentFlags()
	pf.String("log-level", utils.DefaultLogLevel, "Log level: debug, info, or warn")
	pf.String("log-format", utils.DefaultLogFormat, "Log format: text or json")
	res.Bind(pf, "log-level", "log.level")
	res.Bind(pf, "log-format", "log.format")

	rootCmd.AddCommand(
		newInitCmd(h, res),
		newSavepointCmd(h, res),
		newRollbackCmd(h, res),
		newStatusCmd(),
		newMcpCmd(),
	)

	return rootCmd
}

// resolveConfig is the shared prologue of every command that reads an existing
// project: it resolves the effective config, sets up the logger from the result,
// and fails if the project is not yet initialised. (init has its own prologue,
// since it runs before the config file exists.)
func resolveConfig(res *appconfig.Resolver) (config.Config, error) {
	cfg, found, err := res.Resolve()
	if err != nil {
		return cfg, err
	}
	if err := utils.SetDefaultContextLogger(cfg.Log.Level, cfg.Log.Format); err != nil {
		return cfg, err
	}
	if !found {
		return cfg, fmt.Errorf("project not initialized: %s not found (run 'gm init')", res.ConfigPath())
	}
	return cfg, nil
}
