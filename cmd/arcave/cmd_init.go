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

	svc, printer := newInitDependencies(projectRoot)
	h := cli.NewConfigInitHandler(svc, printer)
	return h.Init(cmd.Context(), newInitTemplate(configInitName), configInitOverride)
}

// newInitTemplate is the config init writes: the defaults and the name.
func newInitTemplate(name string) config.Config {
	template := config.NewConfig()
	template.Project.Name = name
	return template
}

func init() {
	flags := configInitCmd.Flags()
	flags.StringVar(&configInitName, "name", "",
		"Project name (default: current directory name)")
	flags.BoolVar(&configInitOverride, "override", false,
		"Overwrite an existing arcave configuration")

	// No flag is bound: --name goes to the template directly and --override is transient.

	rootCmd.AddCommand(configInitCmd)
}
