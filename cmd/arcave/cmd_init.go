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
	"fmt"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/cli"
	"github.com/greenmaskio/arcave/internal/config"
)

var (
	configInitName     string
	configInitOverride bool

	configInitCmd = &cobra.Command{
		Use:   "init",
		Short: "Initialize arcave metadata for the current project",
		Args:  cobra.NoArgs,
		RunE:  runConfigInit,
	}
)

// runConfigInit writes the config the other commands require: a template of the
// defaults and --name, whatever the environment or an existing file says. It names
// no compose project and opens neither the storage backend nor the Docker engine.
func runConfigInit(cmd *cobra.Command, _ []string) error {
	if configFound && !configInitOverride {
		return fmt.Errorf("project already initialized use --override")
	}

	template := config.NewConfig()
	template.Project.Name = configInitName
	svc, printer := newInitDependencies(projectRoot)
	h := cli.NewConfigInitHandler(svc, printer)
	return h.Init(cmd.Context(), template, configInitOverride)
}

func init() {
	flags := configInitCmd.Flags()
	flags.StringVar(&configInitName, "name", "",
		"Project name (default: current directory name)")
	flags.BoolVar(&configInitOverride, "override", false,
		"Overwrite an existing arcave configuration")

	// Neither flag is bound: the template takes --name directly, and --override is
	// transient. The storage is the directory one at its default path.

	rootCmd.AddCommand(configInitCmd)
}
