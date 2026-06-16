package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// configFilePath returns the path to the GM config in the current project.
func configFilePath() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine working directory: %w", err)
	}
	return filepath.Join(cwd, gmDir, gmConfigFile), nil
}

// loadConfig reads and parses .gm/config.yaml from the current project.
func loadConfig() (*projectConfig, error) {
	path, err := configFilePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("project not initialized: %s not found (run 'gm init')", path)
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg projectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}
