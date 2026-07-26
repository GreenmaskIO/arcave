package main

import (
	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/cli"
)

var (
	recommendFlagFiles            []string
	recommendFlagRecommenderFiles []string

	recommendCmd = &cobra.Command{
		Use:   "recommend",
		Short: "Report volumes the project's services should mount but do not",
		Long: "Inspect the compose project and report, as JSON, every persistent volume a " +
			"service's image is known to need and the compose file does not declare — state " +
			"a savepoint would silently fail to capture.\n\n" +
			"It is read-only: it reads compose files and the recommender catalog, and touches " +
			"neither the savepoint store nor the Docker daemon.\n\n" +
			"Exits 1 when there is anything to recommend, 0 when there is not.",
		Args: cobra.NoArgs,
		RunE: runRecommend,
	}
)

// runRecommend needs a config only when the user named no compose file: with an
// explicit -f it reads nothing else from the project, so it runs in a directory
// arcave was never initialised in.
func runRecommend(cmd *cobra.Command, _ []string) error {
	files := recommendFlagFiles
	if len(files) == 0 {
		if err := requireConfig(); err != nil {
			return err
		}
		files = projectCfg.Project.ComposeFiles
	}

	svc, printer := recommendServiceDeps(recommendFlagRecommenderFiles)
	h := cli.NewRecommendHandler(svc, printer)
	found, err := h.Recommend(cmd.Context(), files...)
	if err != nil {
		return err
	}
	if found {
		rootExitCode = 1
	}
	return nil
}

func init() {
	flags := recommendCmd.Flags()
	// Deliberately unbound: --file here steers this one run and must not feed
	// project.compose_files, which `arcave init`'s own -f already owns. Its
	// default is empty rather than docker-compose.yaml, because "the user named
	// no file" is what selects the configured file set.
	flags.StringSliceVarP(&recommendFlagFiles, "file", "f", nil,
		"Docker Compose file (repeatable; default: the project's configured compose files)")
	// Named for the command rather than for the file format: a user who typed
	// `arcave recommend` should not have to know the catalog is called an ARC.
	// Singular and repeatable, like --file above; several are merged left to
	// right, so a personal catalog can layer over a team one.
	flags.StringSliceVar(&recommendFlagRecommenderFiles, "recommender-file", nil,
		"Recommender catalog to merge over the built-in one (repeatable)")

	viperBind(cfgViper, flags, map[string]string{
		"file": "project.compose_files",
	})

	rootCmd.AddCommand(recommendCmd)
}
