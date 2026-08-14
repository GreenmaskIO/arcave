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

	// A key no field claims is a typo, not a value quietly ignored.
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
