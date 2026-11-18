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

// Package config defines arcave's per-project configuration value type, the pure
// helpers that seed and validate it, and Unmarshal — the single home of the
// config policy (unknown-key rejection, defaults, validation).
// Persistence lives in internal/repository/config; the multi-source merge
// (flag > env > file) is cmd/arcave's, which hands the loaded viper here.
//
// The config *is* its viper decode — the mapstructure tags below are the schema
// viper fills — so Unmarshal takes a *viper.Viper rather than re-implementing
// that decode over a settings map. The pluggable sections still declare plain
// local union arms rather than embedding directory.Config or s3.Config:
// embedding would drag a cloud SDK into the import graph of repository/config,
// and therefore of `arcave init`. cmd/arcave translates each section into its package's
// own options struct.
package config

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/greenmaskio/arcave/internal/config/storageconfig"
	"github.com/greenmaskio/arcave/internal/model"
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	"github.com/greenmaskio/arcave/internal/utils"
)

const fileName = "config.yaml"

// ErrInvalid reports a config value arcave cannot act on; every error Unmarshal
// returns wraps it.
var ErrInvalid = errors.New("invalid config")

// DefaultSafetySnapshotRetainRecent is how many safety snapshots a gc leaves each compose project.
const DefaultSafetySnapshotRetainRecent = 1

// DefaultConcurrency is how many volumes, writable layers or images one workflow
// moves at once.
const DefaultConcurrency = 5

// Config is the content persisted to .arcave/config.yaml by init. The yaml tags
// drive Save; the mapstructure tags drive Unmarshal.
// One section per subsystem: a new subsystem adds a section rather than another
// top-level key.
type Config struct {
	Project    Project               `yaml:"project" mapstructure:"project"`
	Common     Common                `yaml:"common" mapstructure:"common"`
	Storage    storageconfig.Storage `yaml:"storage" mapstructure:"storage"`
	Encryption Encryption            `yaml:"encryption,omitempty" mapstructure:"encryption"`
	// ComposeProvider is the compose provider's sections — docker, compose, snapshot,
	// writable_layers, capture, backends — inline, at the top level of the file.
	ComposeProvider composeconfig.Config `yaml:",inline" mapstructure:",squash"`
	Rollback        Rollback             `yaml:"rollback" mapstructure:"rollback"`
	GC              GC                   `yaml:"gc" mapstructure:"gc"`
	// Log holds the project's default logging settings, honoured by every
	// command unless overridden by the --log-level/--log-format global flags.
	Log Log `yaml:"log,omitempty" mapstructure:"log"`
}

// Project names the storage tenant every store root is scoped by. The compose
// compose project is not here: it is named per invocation with -p/--project-name.
type Project struct {
	Name string `yaml:"name" mapstructure:"name"`
}

// Validate gates the name, which becomes the tenant segment of every store
// root. ApplyDefaults has run by then, so an empty name means the project
// directory normalized to nothing and the user has to say what to call it.
func (p Project) Validate() error {
	if err := model.ArcaveProjectName(p.Name).Validate(); err != nil {
		return fmt.Errorf("%w: project.name: %w (set project.name explicitly)", ErrInvalid, err)
	}
	return nil
}

// Encryption encrypts every object arcave writes to the store, to Key. A missing or
// malformed Key is refused where the store opens, not here: init writes Enabled with no
// Key, and a Key in the environment reaches a plain project too, where it is ignored.
type Encryption struct {
	Enabled bool `yaml:"enabled,omitempty" mapstructure:"enabled"`
	// Key is the age identity every object is encrypted to. Secret.
	Key string `yaml:"key,omitempty" mapstructure:"key"`
}

// Common holds the settings every workflow shares.
type Common struct {
	// Concurrency caps how many volumes, writable layers or images a capture or a
	// restore works at once.
	Concurrency int `yaml:"concurrency" mapstructure:"concurrency"`
}

// Validate rejects a concurrency no workflow could run with.
func (c Common) Validate() error {
	if c.Concurrency < 1 {
		return fmt.Errorf("%w: common.concurrency must be at least 1, got %d", ErrInvalid, c.Concurrency)
	}
	return nil
}

// Rollback configures the rollback workflow.
type Rollback struct {
	// SafetySnapshot is whether a rollback captures the live volumes and writable layers first, so
	// it can be undone.
	SafetySnapshot *bool `yaml:"safety_snapshot" mapstructure:"safety_snapshot"`
}

// SkipSafetySnapshot reports whether the config turns the safety snapshot off; unset takes one.
func (r Rollback) SkipSafetySnapshot() bool {
	return r.SafetySnapshot != nil && !*r.SafetySnapshot
}

// GC configures what `arcave gc` retains.
type GC struct {
	SafetySnapshots GCSafetySnapshots `yaml:"safety_snapshots" mapstructure:"safety_snapshots"`
}

// GCSafetySnapshots says how many safety snapshots a gc leaves each compose project.
type GCSafetySnapshots struct {
	// RetainRecent is how many of the newest to keep; 0 deletes every one.
	RetainRecent *int `yaml:"retain_recent" mapstructure:"retain_recent"`
}

// Validate rejects a negative retention.
func (g GC) Validate() error {
	if retainRecent := utils.Value(g.SafetySnapshots.RetainRecent); retainRecent < 0 {
		return fmt.Errorf("%w: gc.safety_snapshots.retain_recent must not be negative, got %d", ErrInvalid, retainRecent)
	}
	return nil
}

// Log is the project's default logging configuration, persisted under the "log"
// key in .arcave/config.yaml.
type Log struct {
	// Level is empty unless set: the output then picks its own, warn above the live tree, else info.
	Level  string `yaml:"level,omitempty" mapstructure:"level"`
	Format string `yaml:"format,omitempty" mapstructure:"format"`
}

// NewConfig returns a Config seeded with the static defaults.
func NewConfig() Config {
	return Config{
		Log: Log{
			Format: utils.DefaultLogFormat,
		},
	}
}

// ApplyDefaults fills the computed defaults that depend on the resolved project
// name, plus the tags of the pluggable sections. It mutates the receiver, so
// callers pass the address of a local Config; no pointer escapes. It is the
// single source of these defaults, shared by resolveConfig (read path) and init
// (write path).
func (c *Config) ApplyDefaults(projectRoot string) {
	if c.Project.Name == "" {
		// The name becomes a store-root segment, so the directory's own spelling is
		// normalized rather than taken verbatim: MyApp yields myapp. A basename that
		// normalizes to nothing stays empty and is reported by Validate, which is the
		// one place that says "name the project explicitly".
		c.Project.Name = model.NormalizeArcaveProjectName(filepath.Base(projectRoot)).String()
	}
	if c.Common.Concurrency == 0 {
		c.Common.Concurrency = DefaultConcurrency
	}
	c.ComposeProvider.ApplyDefaults()
	c.Storage = c.Storage.WithDefaults()
	if c.Rollback.SafetySnapshot == nil {
		c.Rollback.SafetySnapshot = utils.New(true)
	}
	if c.GC.SafetySnapshots.RetainRecent == nil {
		c.GC.SafetySnapshots.RetainRecent = utils.New(DefaultSafetySnapshotRetainRecent)
	}
}

// Validate checks the sections whose values arcave must be able to act on. It runs
// after ApplyDefaults, so an empty section is a default rather than an error.
func (c *Config) Validate() error {
	if err := c.Project.Validate(); err != nil {
		return err
	}
	if err := c.Common.Validate(); err != nil {
		return err
	}
	if err := c.ComposeProvider.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := c.Storage.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return c.GC.Validate()
}

// Path returns the absolute config file path for the project rooted at
// projectRoot.
func Path(projectRoot string) string {
	return filepath.Join(projectRoot, model.StateDir, fileName)
}
