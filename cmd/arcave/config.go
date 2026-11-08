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

// encryptionKeyEnv is the environment variable encryption.key is read from.
const encryptionKeyEnv = "ARCAVE_ENCRYPTION_KEY"

// setupViper points viper at configPath and lets ARCAVE_<KEY> environment variables
// override it, with dots mapped to underscores (ARCAVE_LOG_LEVEL feeds "log.level").
func setupViper(v *viper.Viper, configPath string) {
	v.SetConfigFile(configPath)
	v.SetConfigType("yaml")
	v.SetEnvPrefix("ARCAVE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
}

// resolveConfig reads the config file (if present) and hands the merged sources to
// config.Unmarshal, which owns the policy. found is false when the file is absent.
func resolveConfig(v *viper.Viper, projectRoot string) (config.Config, bool, error) {
	// AutomaticEnv reads a variable only for a key another source names; a bound key is read regardless.
	for _, key := range config.Keys() {
		v.MustBindEnv(key)
	}

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

// resolveWithoutFile resolves the config from flags, env and defaults alone, for
// init: an existing file, broken or not, is only reported as found, never read.
func resolveWithoutFile(v *viper.Viper, projectRoot, configPath string) (config.Config, bool, error) {
	found := true
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		found = false
	} else if err != nil {
		return config.Config{}, false, fmt.Errorf("stat config: %w", err)
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
func viperBind(v *viper.Viper, flags *pflag.FlagSet, flagKeys map[string]string) {
	for name, key := range flagKeys {
		flag := flags.Lookup(name)
		if flag == nil {
			panic(fmt.Sprintf("bind config key %q: no flag %q", key, name))
		}
		if err := v.BindPFlag(key, flag); err != nil {
			panic(fmt.Errorf("bind flag %q: %w", name, err))
		}
	}
}

// viperBindToggle binds a bool flag and its --no-<name> negation to one config key, as viperBind
// binds one flag: the negation, when changed, reads as the flag set to its opposite.
func viperBindToggle(v *viper.Viper, flags *pflag.FlagSet, name, key string) {
	on, off := flags.Lookup(name), flags.Lookup(negatedFlagName(name))
	if on == nil || off == nil {
		panic(fmt.Sprintf("bind config key %q: no flag %q or its negation", key, name))
	}
	if err := v.BindFlagValue(key, toggleFlag{on: on, off: off}); err != nil {
		panic(fmt.Errorf("bind flag %q: %w", name, err))
	}
}

// negatedFlagName names the flag turning a bool flag off.
func negatedFlagName(name string) string {
	return "no-" + name
}

// toggleFlag is a bool flag and its negation, read by viper as one flag.
type toggleFlag struct {
	on  *pflag.Flag
	off *pflag.Flag
}

func (f toggleFlag) HasChanged() bool {
	return f.on.Changed || f.off.Changed
}

func (f toggleFlag) Name() string {
	return f.on.Name
}

func (f toggleFlag) ValueString() string {
	if !f.off.Changed {
		return f.on.Value.String()
	}
	if f.off.Value.String() == "true" {
		return "false"
	}
	return "true"
}

func (f toggleFlag) ValueType() string {
	return "bool"
}
