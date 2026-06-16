package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/greenmaskio/gm-tools/internal/client/compose"
)

const (
	gmDir        = ".gm"
	gmConfigFile = "config.yaml"
)

// projectConfig is the content persisted to .gm/config.yaml by init.
type projectConfig struct {
	Name         string   `yaml:"name"`
	ComposeFiles []string `yaml:"compose_files"`
	SavepointDir string   `yaml:"savepoint_dir"`
}

func newInitCmd() *cobra.Command {
	var (
		name         string
		composeFiles []string
		savepointDir string
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize GM metadata for the current project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(cmd, name, composeFiles, savepointDir)
		},
	}

	cmd.Flags().StringVar(&name, "name", "",
		"Project name (default: current directory name)")
	cmd.Flags().StringSliceVarP(&composeFiles, "file", "f", []string{"docker-compose.yaml"},
		"Docker Compose file (repeatable)")
	cmd.Flags().StringVar(&savepointDir, "savepoint-dir", "",
		"Savepoint backup directory (default: .gm/savepoints/<project_name>)")
	return cmd
}

func runInit(cmd *cobra.Command, name string, composeFiles []string, savepointDir string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}

	configPath := filepath.Join(cwd, gmDir, gmConfigFile)
	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf("project already initialized: %s exists", configPath)
	}

	// Apply defaults that depend on the resolved project name.
	if name == "" {
		name = filepath.Base(cwd)
	}
	if savepointDir == "" {
		savepointDir = filepath.Join(gmDir, "savepoints", name)
	}

	// Read and verify the compose project before writing anything.
	if _, _, err := compose.Load(cmd.Context(), composeFiles...); err != nil {
		return err
	}

	cfg := projectConfig{
		Name:         name,
		ComposeFiles: composeFiles,
		SavepointDir: savepointDir,
	}

	if err := os.MkdirAll(filepath.Join(cwd, gmDir), 0o755); err != nil {
		return fmt.Errorf("create %s directory: %w", gmDir, err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Initialized GM project %q at %s\n", name, configPath)
	return nil
}
