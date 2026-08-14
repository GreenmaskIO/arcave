package main

import (
	"fmt"
	"os"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/config"
	ngcli "github.com/greenmaskio/arcave/internal/ng/cli"
	"github.com/greenmaskio/arcave/internal/utils"
)

// The state every command shares, produced once by initRoot and read from RunE.
// It is package-level because cobra registers commands from init(), so there is
// nowhere earlier to hang it — see config.go for the resolution that fills it.
var (
	projectRoot    string        // the working directory initRoot found
	projectCfg     config.Config // the effective config every command runs on
	configFound    bool          // whether .arcave/config.yaml existed
	configErr      error         // a config file that exists but arcave cannot speak
	rootConfigFile string        // the --config flag's target, read before projectCfg exists

	// The addressing flags every stack-scoped command inherits. Unbound to
	// viper: no Config field claims those keys.
	rootFlagProject   string
	rootFlagSessionID string
	rootFlagProvider  string
	rootFlagStack     string

	// rootExitCode is the status a *successful* command exits with. It is zero
	// unless a command sets it, so nothing changes for the commands that only
	// ever succeed or fail. `recommend` sets 1 when it has advice to give: that
	// is a finding, not an error — it belongs on stdout as JSON, and returning
	// an error to say it would put a second, redundant line on stderr.
	rootExitCode int
)

// The build stamps, filled at link time by goreleaser's -X flags (see
// .goreleaser.yaml). They are plain package vars in main because that is the
// only path -X can name, and they keep readable defaults so a binary built by
// `make build` or `go run` reports what it is rather than an empty string.
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "arcave",
	Short: "Local environment lifecycle manager for stateful Docker Compose applications",
	// Setting Version is what gives the tree its --version flag. Go resolves the
	// initialisation order from the dependency, so the stamps above are already
	// filled when this literal is evaluated. It is the one output that is not
	// JSON: nothing consumes it as a command result, and cobra renders it before
	// PersistentPreRunE has resolved a config to print through.
	Version: fmt.Sprintf("%s (commit %s, built %s)", version, commit, buildDate),
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

	// A config arcave cannot speak is held, not raised: it must not stop
	// `init --override`, which is the fix. The require* guards raise it elsewhere.
	projectCfg, configFound, err = resolveConfig(cfgViper, projectRoot)
	if err != nil {
		configErr, configFound = err, true
		projectCfg, err = fallbackConfig(cfgViper, projectRoot)
		if err != nil {
			return fmt.Errorf("resolve config: %w", err)
		}
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

// rootAddressing hands the four addressing flags to the adapter verbatim.
func rootAddressing() ngcli.Addressing {
	return ngcli.Addressing{
		Project:   rootFlagProject,
		SessionID: rootFlagSessionID,
		Provider:  rootFlagProvider,
		Stack:     rootFlagStack,
	}
}

// requireConfig guards every command that reads the project's own config.
func requireConfig() error {
	if err := requireUsableConfig(); err != nil {
		return err
	}
	if !configFound {
		return fmt.Errorf("config is not found: run 'arcave init' if not yet initialized")
	}
	return nil
}

// requireUsableConfig raises a config file that exists and arcave cannot speak.
func requireUsableConfig() error {
	if configErr != nil {
		return fmt.Errorf("%w (run 'arcave init --override' to replace the config)", configErr)
	}
	return nil
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

	pf.StringVar(&rootFlagProject, "project", "",
		"Project to address (default: project.name from the config)")
	pf.StringVar(&rootFlagSessionID, "session-id", "",
		fmt.Sprintf("Session to work in (default: $%s)", ngcli.EnvSessionID))
	pf.StringVar(&rootFlagProvider, "provider", "",
		"Id of the registered provider to reach (default: the sole project.providers.stacks entry)")
	pf.StringVar(&rootFlagStack, "stack", "",
		"Name of the spec to address; the provider finds what belongs to it")

	viperBind(cfgViper, pf, map[string]string{
		"log-level":  "log.level",
		"log-format": "log.format",
	})
}
