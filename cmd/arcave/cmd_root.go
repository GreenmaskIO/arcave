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
	"context"
	"fmt"
	"os"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
)

const (
	// projectNameEnv is the environment counterpart of -p/--project-name.
	projectNameEnv = "ARCAVE_PROJECT_NAME"
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

	// projectName is the compose project every project-bound command operates on. It
	// is no config key: one project directory may hold several compose projects.
	projectName string
	// sessionRef names the session, by id or name, every project-bound command works in;
	// empty is model.DefaultSession.
	sessionRef string
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

	// The flag feeds no config key, so viper never reads the environment for it.
	if projectName == "" {
		projectName = os.Getenv(projectNameEnv)
	}
	if sessionRef == "" {
		sessionRef = os.Getenv(sessionEnv)
	}

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

// requireProjectName guards every command bound to a compose project.
func requireProjectName() error {
	if projectName == "" {
		return fmt.Errorf("no compose project named: pass -p/--project-name, or set %s", projectNameEnv)
	}
	return nil
}

// withDeps runs fn with the dependencies for the compose project -p named, released when fn
// returns. It takes no lock: core.Service claims the project lock itself.
func withDeps(cmd *cobra.Command, fn func(ctx context.Context, d *dependencies) error) error {
	if err := requireProjectName(); err != nil {
		return err
	}

	ctx := cmd.Context()
	session, err := resolveSession(ctx, sessionRef)
	if err != nil {
		return err
	}
	deps, err := initAllDependencies(ctx, projectRoot, projectCfg, session, model.DockerComposeProjectName(projectName))
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

// resolveSession turns a --session value into the id of a session that exists.
func resolveSession(ctx context.Context, ref string) (model.SessionID, error) {
	if ref == "" {
		return model.DefaultSession, nil
	}
	var record model.Session
	err := withSessionDeps(ctx, func(d *sessionDependencies) error {
		var err error
		record, err = d.service.ResolveSession(ctx, ref)
		return err
	})
	if err != nil {
		return model.SessionID{}, fmt.Errorf("resolve --session: %w", err)
	}
	return record.ID, nil
}

// withSessionDeps runs fn over the session dependencies, which reach no daemon and
// name no compose project.
func withSessionDeps(ctx context.Context, fn func(d *sessionDependencies) error) error {
	deps, err := initSessionDependencies(projectRoot, projectCfg)
	if err != nil {
		return fmt.Errorf("init session dependencies: %w", err)
	}
	defer func() {
		if err := deps.Close(); err != nil {
			log.Ctx(ctx).Error().Err(err).Msg("close session dependencies")
		}
	}()
	return fn(deps)
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&rootConfigFile, "config", "", "config file path")
	// Unbound, like --config: it steers one run rather than describing the project.
	pf.StringVarP(&projectName, "project-name", "p", "",
		fmt.Sprintf("Docker Compose project to operate on, as 'docker compose ls' names it (env %s)", projectNameEnv))
	pf.StringVar(&sessionRef, "session", "",
		fmt.Sprintf("Session to work in, by name or id (default %q; env %s)", model.DefaultSessionName, sessionEnv))
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
	// the compose project inherits them and the binding is unambiguous.
	pf.Duration("wait-timeout", config.DefaultWaitTimeout,
		"How long to wait for services to become healthy after they are started, e.g. 2m or 90s")
	pf.Duration("stop-timeout", config.DefaultStopTimeout,
		"The least time a container gets to exit on its own before it is killed; a service's stop_grace_period raises it, e.g. 30s")
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
