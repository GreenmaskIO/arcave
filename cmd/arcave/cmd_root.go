package main

import (
	"context"
	"fmt"
	"os"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/utils"
)

// defaultUpWaitTimeout is the --wait-timeout default of every command that
// restarts the stack (rollback, savepoint create). The compose client applies
// the same bound itself when a caller passes <= 0, but keeps it private: this is
// the value the flag advertises in `arcave --help`, which is the command layer's
// concern. Keep the two in step — a drift makes the help text lie about what a
// bare command does.
const defaultUpWaitTimeout = 120

// The state every command shares, produced once by initRoot and read from RunE.
// It is package-level because cobra registers commands from init(), so there is
// nowhere earlier to hang it — see config.go for the resolution that fills it.
var (
	projectRoot    string        // the working directory initRoot found
	projectCfg     config.Config // the effective config every command runs on
	configFound    bool          // whether .arcave/config.yaml existed
	rootConfigFile string        // the --config flag's target, read before projectCfg exists
)

var rootCmd = &cobra.Command{
	Use:   "arcave",
	Short: "Local environment lifecycle manager for stateful Docker Compose applications",
	// SilenceUsage keeps a runtime failure from dumping the whole usage text;
	// cobra still prints usage for genuine flag/arg errors.
	SilenceUsage: true,
	// PersistentPreRunE, not PreRunE: cobra runs PreRunE only for the command
	// that actually executes, so a plain PreRunE here would never fire for a
	// subcommand and every command would see a zero-valued config.
	PersistentPreRunE: func(*cobra.Command, []string) error {
		return initRoot()
	},
}

// initRoot is the shared prologue of every command: it locates the project root,
// points viper at the config file, resolves the effective config (flag > env >
// file > default), and sets up the context logger from the result.
func initRoot() error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current working directory: %w", err)
	}
	projectRoot = cwd

	configPath := config.Path(projectRoot)
	if rootConfigFile != "" {
		configPath = rootConfigFile
	}
	setupViper(cfgViper, configPath)

	// A config arcave cannot speak is held, not raised: it must not stop `arcave init
	// --override`, which is the fix the error names. requireInitialized raises it
	// for every command that reads the project instead. Anything else — an
	// unreadable file, an unknown key — is fatal here, since no command can
	// proceed and init would overwrite a file the user may not mean to lose.
	projectCfg, configFound, err = resolveConfig(cfgViper, projectRoot)
	if err != nil {
		return fmt.Errorf("resolve config: %w", err)
	}
	if err := utils.SetDefaultContextLogger(projectCfg.Log.Level, projectCfg.Log.Format); err != nil {
		return fmt.Errorf("set log level: %w", err)
	}
	return nil
}

// requireConfig guards every command that reads an existing project: the
// config must be present and usable. init carries no such guard — it runs before
// the config file exists, and it is how an unusable one is replaced.
func requireConfig() error {
	if !configFound {
		return fmt.Errorf("config is not found: run 'arcave init' if not yet initialized")
	}
	return nil
}

// withDeps runs fn with the savepoint workflows' dependencies, built for this
// command and released when it returns. Every command that touches storage or
// Docker goes through it, so the build/defer-close pair lives in one place.
//
// It takes no lock, and neither does any other command-layer helper: serialising
// a mutating workflow is the workflow's own property, so core.Service claims the
// project lock itself. A lock taken here would be invisible to `arcave mcp`, which
// calls the service directly.
func withDeps(cmd *cobra.Command, fn func(ctx context.Context, d *dependencies) error) error {
	ctx := cmd.Context()
	deps, err := initAllDependencies(ctx, projectRoot, projectCfg)
	if err != nil {
		return fmt.Errorf("init dependencies: %w", err)
	}
	defer func() {
		if err := deps.Close(ctx); err != nil {
			log.Ctx(ctx).Error().Err(err).Msg("close all dependencies")
		}
	}()
	return fn(ctx, deps)
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&rootConfigFile, "config", "", "config file path")
	pf.String("log-format", utils.DefaultLogFormat, "Log format: text or json")
	pf.String("log-level", utils.DefaultLogLevel,
		fmt.Sprintf(
			"logging level %s, %s or %s",
			zerolog.LevelDebugValue,
			zerolog.LevelInfoValue,
			zerolog.LevelWarnValue,
		),
	)

	viperBind(cfgViper, pf, map[string]string{
		"log-level":  "log.level",
		"log-format": "log.format",
	})
}
