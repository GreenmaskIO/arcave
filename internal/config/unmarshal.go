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

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// Unmarshal decodes the sources viper has merged into a validated Config. It is
// the single home of the config policy: unknown-key rejection, defaults,
// validation. cmd/arcave owns only the merge that fills v.
func Unmarshal(v *viper.Viper, projectRoot string) (Config, error) {
	cfg := NewConfig()
	if err := v.Unmarshal(&cfg, rejectUnknownKeys); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
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
