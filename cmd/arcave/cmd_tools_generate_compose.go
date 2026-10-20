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

package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/cli"
	"github.com/greenmaskio/arcave/internal/model"
)

var (
	toolsGenerateComposeOutput        = ""
	toolsGenerateComposeForce         = false
	toolsGenerateComposeAbsolutePaths = false

	toolsGenerateComposeCmd = &cobra.Command{
		Use:   "generate-compose",
		Short: "Generate a docker compose file from the running compose project",
		Long: "Generate a docker compose file from what the Docker daemon reports about the compose project's " +
			"containers, volumes and networks, to the file -o names. The file carries every environment " +
			"value the containers run with, secrets included.",
		Args: cobra.NoArgs,
		RunE: runToolsGenerateCompose,
	}
)

func runToolsGenerateCompose(cmd *cobra.Command, _ []string) error {
	return withToolsDeps(cmd, func(ctx context.Context, d *toolsDependencies) error {
		h := cli.NewToolsGenerateComposeHandler(d.service, d.printer)
		return h.GenerateCompose(ctx, model.GenerateComposeRequest{AbsolutePaths: toolsGenerateComposeAbsolutePaths},
			toolsGenerateComposeOutput, toolsGenerateComposeForce)
	})
}

func init() {
	flags := toolsGenerateComposeCmd.Flags()
	flags.StringVarP(&toolsGenerateComposeOutput, "output", "o", "", "file to write the compose file to")
	flags.BoolVar(&toolsGenerateComposeForce, "force", false, "overwrite the output file if it exists")
	flags.BoolVar(&toolsGenerateComposeAbsolutePaths, "absolute-paths", false,
		"keep bind sources absolute instead of relative to the output file's directory")
	// A missing flag is a wiring bug, as viperBind treats one.
	if err := toolsGenerateComposeCmd.MarkFlagRequired("output"); err != nil {
		panic(err)
	}
	toolsCmd.AddCommand(toolsGenerateComposeCmd)
}
