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

package config

import (
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/buildkite/interpolate"
	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// Unmarshal decodes the sources viper has merged into a validated Config. It is
// the single home of the config policy: unknown-key rejection, ${VAR}
// interpolation, defaults, validation. cmd/arcave owns only the merge that fills v.
func Unmarshal(v *viper.Viper, projectRoot string) (Config, error) {
	cfg := NewConfig()
	if err := v.Unmarshal(&cfg, rejectUnknownKeys, interpolateEnvVars); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	cfg.ApplyDefaults(projectRoot)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// rejectUnknownKeys fails the decode on a key no field claims, leaving viper's
// own decoder defaults — the duration and comma-slice hooks — in place.
func rejectUnknownKeys(dc *mapstructure.DecoderConfig) { dc.ErrorUnused = true }

// interpolateEnvVars expands ${VAR}, ${VAR:-default} and ${VAR?message} in every
// string value before viper's own hooks convert it, so a file can name a secret
// it does not hold. The hook sees merged values, so an ARCAVE_* variable or a flag
// is interpolated too: a literal $ is $$ in every source.
func interpolateEnvVars(dc *mapstructure.DecoderConfig) {
	dc.DecodeHook = mapstructure.ComposeDecodeHookFunc(interpolateEnvHook, dc.DecodeHook)
}

// interpolateEnvHook refuses a bare $VAR or ${VAR} whose variable is unset, rather
// than expanding it to nothing: a secret with a stray $ in it would otherwise lose
// its tail silently. ${VAR:-} says "empty" explicitly, $$ is a literal $.
func interpolateEnvHook(_, _ reflect.Type, data any) (any, error) {
	value, ok := data.(string)
	if !ok || !strings.Contains(value, "$") {
		return data, nil
	}
	expression, err := interpolate.NewParser(value).Parse()
	if err != nil {
		return nil, fmt.Errorf("interpolate environment variable: %w", err)
	}
	env := interpolate.NewSliceEnv(os.Environ())
	for _, item := range expression {
		var identifier string
		switch expansion := item.Expansion.(type) {
		case interpolate.VariableExpansion:
			identifier = expansion.Identifier
		case interpolate.SubstringExpansion:
			identifier = expansion.Identifier
		default:
			continue
		}
		if _, set := env.Get(identifier); !set {
			return nil, fmt.Errorf(
				"environment variable %s is not set (write ${%s:-} for an empty default, or $$ for a literal $)",
				identifier, identifier)
		}
	}
	expanded, err := expression.Expand(env)
	if err != nil {
		return nil, fmt.Errorf("interpolate environment variable: %w", err)
	}
	return expanded, nil
}
