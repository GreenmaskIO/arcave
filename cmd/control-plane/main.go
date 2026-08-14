// Command control-plane is the arcave service: it registers the providers its
// config names and serves the API the CLI reaches it over.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/utils"
)

// The build stamps, filled at link time by goreleaser's -X flags.
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "control-plane",
	Short: "The arcave control plane: the provider registry and its API",
	// Setting Version is what gives the tree its --version flag.
	Version: fmt.Sprintf("%s (commit %s, built %s)", version, commit, buildDate),
	// SilenceUsage keeps a runtime failure from dumping the whole usage text.
	SilenceUsage: true,
	// PersistentPreRunE, not PreRunE: cobra runs PreRunE only for the command that executes.
	PersistentPreRunE: func(*cobra.Command, []string) error {
		return initRoot()
	},
}

// initRoot is the shared prologue of every command: resolve the effective config
// and set up the context logger from it.
func initRoot() error {
	path, explicit := configPath()

	var err error
	cpCfg, configFound, err = resolveConfig(cpViper, path, explicit)
	if err != nil {
		return fmt.Errorf("resolve config: %w", err)
	}

	logger, err := utils.SetDefaultContextLogger(cpCfg.Log.Level, cpCfg.Log.Format)
	if err != nil {
		return fmt.Errorf("set log level: %w", err)
	}

	// Before any provider reaches docker or compose: they log through logrus until this runs.
	utils.BridgeLogrus(logger, logger.GetLevel())
	return nil
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&rootConfigFile, "config", "",
		fmt.Sprintf("config file path (default: $%s, else %s)", envConfigFile, defaultConfigPath))
	pf.String("log-format", utils.DefaultLogFormat, "Log format: text or json")
	pf.String("log-level", utils.DefaultLogLevel,
		fmt.Sprintf(
			"logging level %s, %s or %s",
			zerolog.LevelDebugValue,
			zerolog.LevelInfoValue,
			zerolog.LevelWarnValue,
		),
	)

	viperBind(cpViper, pf, map[string]string{
		"log-level":  "log.level",
		"log-format": "log.format",
	})
}

func main() {
	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		// cobra has already reported the error on stderr.
		os.Exit(1)
	}
}
