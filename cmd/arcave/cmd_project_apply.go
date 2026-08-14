package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/greenmaskio/arcave/internal/config"
	ngcli "github.com/greenmaskio/arcave/internal/ng/cli"
)

var (
	projectApplyFile string

	projectApplyCmd = &cobra.Command{
		Use:   "apply",
		Short: "Create or update the project from the config's project section",
		Args:  cobra.NoArgs,
		RunE:  runProjectApply,
	}
)

func runProjectApply(cmd *cobra.Command, _ []string) error {
	cfg, err := projectApplyConfig()
	if err != nil {
		return err
	}

	return ngcli.NewProjectApplyHandler(newAPI(), newPrinter()).Apply(cmd.Context(), cfg)
}

// projectApplyConfig returns the config to apply: the project's own, or the
// file -f names, which must then exist.
func projectApplyConfig() (config.Config, error) {
	if projectApplyFile == "" {
		if err := requireConfig(); err != nil {
			return config.Config{}, err
		}
		return projectCfg, nil
	}

	v := viper.New()
	setupViper(v, projectApplyFile)
	cfg, found, err := resolveConfig(v, projectRoot)
	if err != nil {
		return config.Config{}, err
	}
	if !found {
		return config.Config{}, fmt.Errorf("config file %q not found", projectApplyFile)
	}
	return cfg, nil
}

func init() {
	projectApplyCmd.Flags().StringVarP(&projectApplyFile, "file", "f", "",
		"Config file to apply instead of the project's own")

	projectCmd.AddCommand(projectApplyCmd)
}
