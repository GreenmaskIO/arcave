package controlplane

import (
	"fmt"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// Unmarshal decodes the merged sources into a validated Config: unknown-key
// rejection, defaults, validation.
func Unmarshal(v *viper.Viper) (Config, error) {
	cfg := NewConfig()

	// A key no field claims is a typo. It reaches no provider key: everything
	// under settings lands in the entry's RawSettings, and the strictness there
	// is the provider's, applied where the keys are understood.
	if err := v.Unmarshal(&cfg, rejectUnknownKeys); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// rejectUnknownKeys fails the decode on a key no field claims, leaving viper's
// own decoder defaults — the duration and comma-slice hooks — in place.
func rejectUnknownKeys(dc *mapstructure.DecoderConfig) { dc.ErrorUnused = true }
