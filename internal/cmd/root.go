// Package cmd defines GM's Cobra command tree: flags, usage, and short help.
// Each command's RunE delegates to the CLI adapter, which calls the services.
package cmd

import (
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/greenmaskio/gm-tools/internal/adapter/cli"
	"github.com/greenmaskio/gm-tools/internal/utils"
)

// NewRootCmd builds the gm command tree wired to the given CLI handlers.
func NewRootCmd(h *cli.Handlers) *cobra.Command {
	logLevel := zerolog.LevelInfoValue
	logFormat := utils.LogFormatText

	root := &cobra.Command{
		Use:   "gm",
		Short: "Local environment lifecycle manager for stateful Docker Compose applications",
		// Configure the global logger before any command runs, so every service
		// call logs at the requested level and format. Logs go to stderr; stdout
		// stays reserved for each command's JSON output. Precedence: an explicit
		// --log-level/--log-format flag wins; otherwise the project config's
		// persisted settings apply; otherwise the built-in defaults.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			level, format := logLevel, logFormat
			if cfgLevel, cfgFormat, ok := h.ConfigLog(); ok {
				if !cmd.Flags().Changed("log-level") && cfgLevel != "" {
					level = cfgLevel
				}
				if !cmd.Flags().Changed("log-format") && cfgFormat != "" {
					format = cfgFormat
				}
			}
			return utils.SetDefaultContextLogger(level, format)
		},
	}

	root.PersistentFlags().StringVar(&logLevel, "log-level", logLevel,
		"Log level: debug, info, or warn")
	root.PersistentFlags().StringVar(&logFormat, "log-format", logFormat,
		"Log format: text or json")

	root.AddCommand(
		newInitCmd(h),
		newSavepointCmd(h),
		newRollbackCmd(h),
		newStatusCmd(),
		newMcpCmd(),
	)

	return root
}
