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

// The whole config resolution, in precedence order: a changed flag, an
// ARCAVE_<KEY> variable, .arcave/config.yaml, the flag's default.

// cfgViper collects the sources. A package variable because commands bind their
// flags to it from init(), long before initRoot resolves anything.
var cfgViper = viper.New()

// setupViper points viper at configPath and lets ARCAVE_<KEY> variables
// override it, dots mapped to underscores (ARCAVE_LOG_LEVEL feeds "log.level").
func setupViper(v *viper.Viper, configPath string) {
	v.SetConfigFile(configPath)
	v.SetConfigType("yaml")
	v.SetEnvPrefix("ARCAVE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
}

// resolveConfig reads the file and hands the loaded viper to config.Unmarshal,
// which owns the policy. found is false when the file is absent.
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

// fallbackConfig resolves the config when the file was refused: the same sources
// minus the file. An empty document is read over it — viper cannot forget one.
func fallbackConfig(v *viper.Viper, projectRoot string) (config.Config, error) {
	if err := v.ReadConfig(strings.NewReader("")); err != nil {
		return config.Config{}, fmt.Errorf("reset config sources: %w", err)
	}
	return config.Unmarshal(v, projectRoot)
}

// viperBind binds each flag to its config key. Only bound flags feed the config,
// which keeps transient flags out of it; a missing flag is a wiring bug.
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
