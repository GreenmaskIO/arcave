package appconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
)

// boundResolver builds a resolver for root and a flag set carrying the flags a
// command may bind — the root-persistent log flags plus init's flags — each
// bound to its config key exactly as the real commands do. Only changed flags
// override lower-precedence sources.
func boundResolver(root string) (*Resolver, *pflag.FlagSet) {
	res := NewResolver(root)
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("log-level", "info", "")
	fs.String("log-format", "text", "")
	fs.String("name", "", "")
	fs.StringSlice("file", []string{"docker-compose.yaml"}, "")
	fs.String("savepoint-dir", "", "")
	res.Bind(fs, "log-level", "log.level")
	res.Bind(fs, "log-format", "log.format")
	res.Bind(fs, "name", "name")
	res.Bind(fs, "file", "compose_files")
	res.Bind(fs, "savepoint-dir", "savepoint_dir")
	return res, fs
}

func writeConfig(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".gm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveDefaultsWhenNoFile(t *testing.T) {
	root := t.TempDir()
	res, _ := boundResolver(root)
	cfg, found, err := res.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("found = true for a missing config file")
	}
	if cfg.Name != filepath.Base(root) {
		t.Errorf("Name = %q, want %q (dir base)", cfg.Name, filepath.Base(root))
	}
	if want := filepath.Join(".gm", "savepoints", cfg.Name); cfg.SavepointDir != want {
		t.Errorf("SavepointDir = %q, want %q", cfg.SavepointDir, want)
	}
	if cfg.Log.Level != "info" || cfg.Log.Format != "text" {
		t.Errorf("Log = %+v, want info/text", cfg.Log)
	}
}

func TestResolveReadsFile(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "name: proj\ncompose_files:\n  - a.yaml\n  - b.yaml\nsavepoint_dir: /snaps\nlog:\n  level: warn\n  format: json\n")
	res, _ := boundResolver(root)
	cfg, found, err := res.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("found = false for an existing config file")
	}
	if cfg.Name != "proj" || cfg.SavepointDir != "/snaps" {
		t.Errorf("cfg = %+v, want name=proj savepoint_dir=/snaps", cfg)
	}
	if len(cfg.ComposeFiles) != 2 || cfg.ComposeFiles[0] != "a.yaml" {
		t.Errorf("ComposeFiles = %v, want [a.yaml b.yaml]", cfg.ComposeFiles)
	}
	if cfg.Log.Level != "warn" || cfg.Log.Format != "json" {
		t.Errorf("Log = %+v, want warn/json", cfg.Log)
	}
}

func TestResolvePrecedenceFlagBeatsFile(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "name: proj\nlog:\n  level: warn\n")
	res, fs := boundResolver(root)
	if err := fs.Set("log-level", "debug"); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := res.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level = %q, want debug (changed flag beats file)", cfg.Log.Level)
	}
}

func TestResolvePrecedenceEnvBeatsFileFlagBeatsEnv(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "log:\n  level: warn\n")
	t.Setenv("GM_LOG_LEVEL", "debug")

	// env beats file when the flag is unchanged.
	res, _ := boundResolver(root)
	cfg, _, err := res.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level = %q, want debug (env beats file)", cfg.Log.Level)
	}

	// a changed flag beats env.
	res, fs := boundResolver(root)
	if err := fs.Set("log-level", "warn"); err != nil {
		t.Fatal(err)
	}
	cfg, _, err = res.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Level != "warn" {
		t.Errorf("Log.Level = %q, want warn (flag beats env)", cfg.Log.Level)
	}
}

// Only bound flags feed the config: an unbound flag, even when changed, must not
// override a lower-precedence source. This is what keeps appconfig agnostic of
// flag names — a command's transient flags stay out of the config.
func TestResolveIgnoresUnboundFlags(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "log:\n  level: warn\n")
	res := NewResolver(root)
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("log-level", "info", "") // deliberately NOT bound via res.Bind
	if err := fs.Set("log-level", "debug"); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := res.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Level != "warn" {
		t.Errorf("Log.Level = %q, want warn (unbound flag must not feed config)", cfg.Log.Level)
	}
}

// Bind panics on a missing flag, since that is a wiring bug rather than a runtime
// condition.
func TestBindPanicsOnMissingFlag(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Bind did not panic on a missing flag")
		}
	}()
	res := NewResolver(t.TempDir())
	res.Bind(pflag.NewFlagSet("empty", pflag.ContinueOnError), "nope", "nope")
}
