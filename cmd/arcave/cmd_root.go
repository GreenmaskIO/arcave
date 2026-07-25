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
	logger, err := utils.SetDefaultContextLogger(projectCfg.Log.Level, projectCfg.Log.Format)
	if err != nil {
		return fmt.Errorf("set log level: %w", err)
	}

	// The bridge goes in here, the moment the logger exists and before any
	// command has had a chance to reach docker or compose: those libraries log
	// through the package-level logrus logger, and every record written before
	// this call would leave in logrus's own format on a stream arcave does not
	// control.
	utils.BridgeLogrus(logger, logger.GetLevel())
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

	// The compose bounds are persistent flags on root, next to --log-level, for
	// the same reason: they are *config keys*, and one viper key can hold exactly
	// one flag binding. Declared per command they would collide — `savepoint
	// create` and `rollback` both want --wait-timeout, and whichever init() ran
	// last would silently own the key for both, so the other command's flag would
	// parse and then be ignored. Declared once here, every command that restarts
	// the stack inherits them and the binding is unambiguous.
	pf.Duration("wait-timeout", config.DefaultWaitTimeout,
		"How long to wait for services to become healthy after they are started, e.g. 2m or 90s")
	pf.Duration("stop-timeout", config.DefaultStopTimeout,
		"How long a container gets to exit on its own before it is killed, e.g. 30s")
	pf.String("progress", config.ProgressLog,
		fmt.Sprintf("Where docker compose's own progress goes: %s (arcave log records), %s (compose's own renderer) or %s",
			config.ProgressLog, config.ProgressTTY, config.ProgressQuiet))

	viperBind(cfgViper, pf, map[string]string{
		"log-level":    "log.level",
		"log-format":   "log.format",
		"wait-timeout": "compose.wait_timeout",
		"stop-timeout": "compose.stop_timeout",
		"progress":     "compose.progress",
	})
}
