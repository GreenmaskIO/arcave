package config

import (
	"fmt"

	"github.com/go-viper/mapstructure/v2"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/spf13/viper"
)

// Unmarshal decodes the sources viper has merged into a validated Config. It is
// the single home of the config policy: version gate, unknown-key rejection,
// defaults, validation. cmd/arcave owns only the merge that fills v.
//
// arcave speaks exactly one schema version, so a file naming any other is refused
// with ErrUnsupportedConfigVersion.
func Unmarshal(v *viper.Viper, projectRoot string) (Config, error) {
	cfg := NewConfig()

	// The gate reads the version through a probe carrying nothing but Version,
	// never through Config: a schema arcave does not speak may spell an existing key
	// with another type, and decoding the whole Config leniently just to read the
	// version would fail on that type rather than on the version. The probe claims
	// one key and ignores the rest.
	//
	// The probe is seeded with the current version, so a source that names none —
	// a fresh project with no config file, resolved from flags and defaults alone —
	// reads as current. Only a version that is present and different is stale.
	var probe struct {
		Version int `mapstructure:"version"`
	}
	probe.Version = Version
	if err := v.Unmarshal(&probe); err != nil {
		return Config{}, fmt.Errorf("parse config version: %w", err)
	}
	if probe.Version != Version {
		return Config{}, fmt.Errorf("expected version '%d' got '%d': %w",
			Version, probe.Version, model.ErrUnsupportedConfigVersion)
	}

	// The version is current, so a key no field claims is a typo.
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
