package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	cpconfig "github.com/greenmaskio/arcave/internal/config/controlplane"
)

// The whole config resolution, in precedence order: a changed flag, an
// ARCAVE_CONTROL_PLANE_<KEY> variable, the config file, the flag's default.

// The state every command reads, produced once by initRoot.
var (
	cpCfg          cpconfig.Config // the effective config every command runs on
	configFound    bool
	rootConfigFile string // the --config flag's target, read before cpCfg exists
)

// cpViper collects the sources. A package variable because commands bind their
// flags to it from init(), long before initRoot resolves anything.
var cpViper = viper.New()

const (
	// envConfigFile names the config file when --config does not.
	envConfigFile = "ARCAVE_CONTROL_PLANE_CONFIG"
	// defaultConfigPath is where the file lives when neither --config nor the env var says.
	defaultConfigPath = "~/.arcave/control-plane.yaml"
	// configFileName is defaultConfigPath's tail, joined to the resolved home.
	configFileName = "control-plane.yaml"
	// stateDirName is the directory it lives in, under the home.
	stateDirName = ".arcave"
)

// configPath resolves the config location: --config, else the env var, else
// ~/.arcave/control-plane.yaml. explicit makes an absent file an error.
func configPath() (path string, explicit bool) {
	if rootConfigFile != "" {
		return rootConfigFile, true
	}
	if fromEnv := os.Getenv(envConfigFile); fromEnv != "" {
		return fromEnv, true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(stateDirName, configFileName), false
	}
	return filepath.Join(home, stateDirName, configFileName), false
}

// setupViper points viper at path and lets ARCAVE_CONTROL_PLANE_<KEY> variables
// override it, dots mapped to underscores.
func setupViper(v *viper.Viper, path string) {
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	v.SetEnvPrefix("ARCAVE_CONTROL_PLANE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
}

// resolveConfig reads the file and hands the loaded viper to Unmarshal, which
// owns the policy. An absent default file is fine; an absent explicit one is not.
func resolveConfig(v *viper.Viper, path string, explicit bool) (cpconfig.Config, bool, error) {
	setupViper(v, path)

	found := true
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		switch {
		case !errors.As(err, &notFound) && !errors.Is(err, os.ErrNotExist):
			return cpconfig.Config{}, false, fmt.Errorf("read config %s: %w", path, err)
		case explicit:
			return cpconfig.Config{}, false, fmt.Errorf("read config %s: %w", path, err)
		default:
			found = false
		}
	}

	cfg, err := cpconfig.Unmarshal(v)
	if err != nil {
		return cpconfig.Config{}, false, fmt.Errorf("unmarshal config %s: %w", path, err)
	}
	return cfg, found, nil
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
