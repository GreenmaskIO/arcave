package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/greenmaskio/arcave/internal/config"
)

// Every command runs on one config, resolved once by rootCmd's PersistentPreRunE
// from four sources, highest precedence first: a changed flag, an ARCAVE_<KEY>
// environment variable, .arcave/config.yaml, the flag's default. This file is the
// whole of that resolution; the values it produces live in cmd_root.go, where the
// commands read them.
//
// Each function takes the viper instance to work on rather than reaching for the
// package-level one, so a test hands it a fresh viper instead of resetting global
// state between cases.

// cfgViper collects the sources. It is a package variable because commands bind
// their flags to it from init(), which runs long before initRoot resolves
// anything.
var cfgViper = viper.New()

// setupViper points viper at configPath and lets ARCAVE_<KEY> environment variables
// override it, with dots mapped to underscores (ARCAVE_LOG_LEVEL feeds "log.level").
func setupViper(v *viper.Viper, configPath string) {
	v.SetConfigFile(configPath)
	v.SetConfigType("yaml")
	v.SetEnvPrefix("ARCAVE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
}

// resolveConfig reads the config file (if present) and hands the loaded viper to
// config.Unmarshal, which owns the policy — version gate, unknown-key rejection,
// defaults, validation. This function owns only the sources: the file read, the
// multi-source merge, and the config path for the error message.
//
// found is false when the config file is absent — a normal case for `arcave init`
// (it proceeds on defaults plus flags) and an error the read commands report via
// requireConfig. It also gates the version check: a fresh project has no
// version to check, and init stamps one when it writes.
func resolveConfig(v *viper.Viper, projectRoot string) (config.Config, bool, error) {
	found := true
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if errors.As(err, &notFound) || errors.Is(err, os.ErrNotExist) {
			found = false
		} else {
			return config.Config{}, false, fmt.Errorf("read config: %w", err)
		}
	}

	cfg, err := config.Unmarshal(v, projectRoot)
	if err != nil {
		return config.Config{}, false, fmt.Errorf("unmarshal config: %w", err)
	}
	return cfg, found, nil
}

// viperBind binds each flag to its config key, so resolveConfig reads it with
// viper precedence — a changed flag > env > file > the flag's default. Only bound
// flags feed the config, which keeps a command's transient flags (--override,
// --dry-run) out of it. A missing flag is a wiring bug, so this panics rather than
// silently dropping the binding.
func viperBind(v *viper.Viper, flags *pflag.FlagSet, flag2cfg map[string]string) {
	for name, key := range flag2cfg {
		flag := flags.Lookup(name)
		if flag == nil {
			panic(fmt.Sprintf("bind config key %q: no flag %q", key, name))
		}
		if err := v.BindPFlag(key, flag); err != nil {
			panic(fmt.Errorf("bind flag %q: %w", name, err))
		}
	}
}
