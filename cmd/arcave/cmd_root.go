// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
	"github.com/greenmaskio/arcave/pkg/progress"
)

const (
	// composeProjectEnv is the environment counterpart of -p/--project-name; ARCAVE_PROJECT_NAME is
	// the config key project.name.
	composeProjectEnv = "ARCAVE_COMPOSE_PROJECT_NAME"
	// sessionEnv is the environment counterpart of --session.
	sessionEnv = "ARCAVE_SESSION"
)

// The state every command shares, produced once by initRoot and read from RunE.
// It is package-level because cobra registers commands from init(), so there is
// nowhere earlier to hang it — see config.go for the resolution that fills it.
var (
	projectRoot    string        // the working directory initRoot found
	projectCfg     config.Config // the effective config every command runs on
	configFound    bool          // whether .arcave/config.yaml existed
	rootConfigFile string        // the --config flag's target, read before projectCfg exists

	// composeProjectFlag is the compose project every project-bound command operates on. It
	// is no config key: one project directory may hold several compose projects.
	composeProjectFlag string
	// sessionRef names the session, by id or name, every project-bound command works in;
	// empty is the current session, else model.DefaultSession.
	sessionRef string
	// sessionRefSource is what named sessionRef: --session or ARCAVE_SESSION; zero when empty.
	sessionRefSource model.SessionRefSource
	// progressMode is --progress as given; runProgressMode the mode initRoot resolved it to.
	progressMode    string
	runProgressMode string
	// resultFormat is --format as given.
	resultFormat string
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
	// main prints the error, once the output is closed: cobra would write it over a live tree.
	SilenceErrors: true,
	// PersistentPreRunE, not PreRunE: cobra runs PreRunE only for the command
	// that actually executes, so a plain PreRunE here would never fire for a
	// subcommand and every command would see a zero-valued config.
	PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		if err := initRoot(cmd); err != nil {
			return err
		}
		// One tracker per run: every workflow the command calls opens its root on it.
		cmd.SetContext(progress.WithTracker(cmd.Context(), progress.New(loadRunOutput(), time.Now)))
		return nil
	},
}

// initRoot resolves the config (flag > env > file > default) and opens the run's output and logger.
// For init it reads no file and only notes whether one exists.
func initRoot(cmd *cobra.Command) error {
	isInitCommand := cmd == configInitCmd
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

	readFlagEnvs()

	if isInitCommand {
		projectCfg, configFound, err = resolveWithoutFile(cfgViper, projectRoot, configPath)
	} else {
		projectCfg, configFound, err = resolveConfig(cfgViper, projectRoot)
	}
	if err != nil {
		return fmt.Errorf("resolve config: %w", err)
	}
	// The server's stdout is its JSON-RPC channel, and its progress goes to its log.
	mode, format := progressModeLog, formatJSON
	if cmd != mcpCmd {
		if mode, err = resolveProgressMode(progressMode, isTerminal(os.Stderr), projectCfg.Log.Format); err != nil {
			return err
		}
		if format, err = resolveFormat(resultFormat, mode, isTerminal(os.Stdout)); err != nil {
			return err
		}
	}
	result := resultOptions{
		format:         format,
		composeProject: composeProjectFlag,
		session:        sessionRef,
		profile:        colorprofile.Detect(os.Stdout, os.Environ()),
	}
	// Once per run: the logger writes through the output, so the context logger never changes.
	out, logger, err := openOutput(mode, projectCfg.Log.Level, projectCfg.Log.Format, result)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	runProgressMode = mode
	openedOutput.Store(out)

	// The bridge goes in here, the moment the logger exists and before any
	// command has had a chance to reach docker or compose: those libraries log
	// through the package-level logrus logger, and every record written before
	// this call would leave in logrus's own format on a stream arcave does not
	// control.
	utils.BridgeLogrus(logger, logger.GetLevel())
	warnIgnoredCDCKeys(logger, projectCfg.Backends.CDC)
	return nil
}

// readFlagEnvs fills each flag left empty from its variable: they feed no config key, so viper never
// reads the environment for them.
func readFlagEnvs() {
	composeProjectFlag = cmp.Or(composeProjectFlag, os.Getenv(composeProjectEnv))
	sessionRef, sessionRefSource = readSessionRef(sessionRef, os.Getenv(sessionEnv))
	progressMode = cmp.Or(progressMode, os.Getenv(progressEnv))
	resultFormat = cmp.Or(resultFormat, os.Getenv(formatEnv))
}

// warnIgnoredCDCKeys warns, once per run, of the tcp transport's keys set under another.
func warnIgnoredCDCKeys(logger zerolog.Logger, cdc *config.CDCBackend) {
	if cdc == nil {
		return
	}
	if keys := cdc.IgnoredTCPKeys(); len(keys) > 0 {
		logger.Warn().Strs("keys", keys).Str("transport", cdc.Transport).
			Msg("backends.cdc keys ignored: they configure the tcp transport only")
	}
}

// readSessionRef is the session ref --session gives, else ARCAVE_SESSION's, and what named it.
func readSessionRef(flag, env string) (string, model.SessionRefSource) {
	switch {
	case flag != "":
		return flag, model.SessionRefSourceFlag
	case env != "":
		return env, model.SessionRefSourceEnv
	default:
		return "", ""
	}
}

// currentSessionRef is the session a command works in when it names none, and what named it.
func currentSessionRef() model.CurrentSessionRef {
	return model.CurrentSessionRef{
		Ref:    sessionRef,
		Source: sessionRefSource,
	}
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

// requireComposeProject is the compose project -p names; a command bound to one gets its name here.
func requireComposeProject() (model.DockerComposeProjectName, error) {
	if composeProjectFlag == "" {
		return "", fmt.Errorf("no compose project named: pass -p/--project-name, or set %s", composeProjectEnv)
	}
	return model.DockerComposeProjectName(composeProjectFlag), nil
}

// resolveSession turns a --session value into the id of a session that exists, for a command that
// opens no compose project; "" is the current session, else the default.
func resolveSession(ctx context.Context, ref string) (model.SessionID, error) {
	var opened closers
	defer opened.release(ctx)

	svc, err := openSessionService(ctx, projectRoot, projectCfg, &opened)
	if err != nil {
		return model.SessionID{}, fmt.Errorf("init session dependencies: %w", err)
	}
	record, err := svc.ResolveSession(ctx, ref)
	if err != nil {
		return model.SessionID{}, err
	}
	return record.ID, nil
}

func init() {
	persistentFlags := rootCmd.PersistentFlags()
	persistentFlags.StringVar(&rootConfigFile, "config", "", "config file path")
	// Unbound, like --config: it steers one run rather than describing the project.
	persistentFlags.StringVarP(&composeProjectFlag, "project-name", "p", "",
		fmt.Sprintf("Docker Compose project to operate on, as 'docker compose ls' names it (env %s)", composeProjectEnv))
	persistentFlags.StringVar(&sessionRef, "session", "",
		fmt.Sprintf("Session to work in, by name or id (default: the current session, see 'arcave session set', else %q; env %s)",
			model.DefaultSessionName, sessionEnv))
	persistentFlags.String("log-format", utils.DefaultLogFormat, "Log format: text or json")
	persistentFlags.String("log-level", "",
		fmt.Sprintf(
			"logging level %s, %s or %s (default: %s above the live tree, else %s)",
			zerolog.LevelDebugValue,
			zerolog.LevelInfoValue,
			zerolog.LevelWarnValue,
			zerolog.LevelWarnValue,
			utils.DefaultLogLevel,
		),
	)

	// The compose bounds are persistent flags on root, next to --log-level, for
	// the same reason: they are *config keys*, and one viper key can hold exactly
	// one flag binding. Declared per command they would collide — `savepoint
	// create` and `rollback` both want --wait-timeout, and whichever init() ran
	// last would silently own the key for both, so the other command's flag would
	// parse and then be ignored. Declared once here, every command that restarts
	// the compose project inherits them and the binding is unambiguous.
	persistentFlags.Duration("wait-timeout", config.DefaultWaitTimeout,
		"How long to wait for services to become healthy after they are started, e.g. 2m or 90s")
	persistentFlags.Duration("stop-timeout", config.DefaultStopTimeout,
		"The least time a container gets to exit on its own before it is killed; a service's stop_grace_period raises it, e.g. 30s")
	// Unbound, like --session: how one run renders, not what the project is.
	persistentFlags.StringVar(&progressMode, "progress", "",
		fmt.Sprintf("How progress is shown: %s (%s on a terminal, else %s), %s (a live tree on stderr), "+
			"%s (log records) or %s (JSON lines on stdout, the result last; env %s)",
			progressModeAuto, progressModeTTY, progressModeLog, progressModeTTY, progressModeLog, progressModeJSON, progressEnv))
	persistentFlags.StringVar(&resultFormat, "format", "",
		fmt.Sprintf("How the result is printed on stdout: %s (%s while the tree is drawn on a terminal stdout, "+
			"else %s), %s or %s (env %s)",
			formatAuto, formatText, formatJSON, formatJSON, formatText, formatEnv))
	persistentFlags.String("compose-progress", config.ProgressLog,
		fmt.Sprintf("Where docker compose's own progress goes: %s (arcave log records), %s (compose's own renderer) or %s",
			config.ProgressLog, config.ProgressTTY, config.ProgressQuiet))

	viperBind(cfgViper, persistentFlags, map[string]string{
		"log-level":        "log.level",
		"log-format":       "log.format",
		"wait-timeout":     "compose.wait_timeout",
		"stop-timeout":     "compose.stop_timeout",
		"compose-progress": "compose.progress",
	})
}
