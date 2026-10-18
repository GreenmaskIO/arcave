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
)

var (
	configInitOverride bool

	configInitCmd = &cobra.Command{
		Use:   "init",
		Short: "Initialize arcave metadata for the current project",
		Args:  cobra.NoArgs,
		RunE:  runConfigInit,
	}
)

// runConfigInit creates the config the other commands require: it names no compose project
// and opens neither the storage backend nor the Docker engine.
func runConfigInit(cmd *cobra.Command, _ []string) error {
	if configFound && !configInitOverride {
		return fmt.Errorf("project already initialized use --override")
	}

	svc, printer := newInitDependencies(projectRoot)
	h := cli.NewConfigInitHandler(svc, printer)
	return h.Init(cmd.Context(), projectCfg, configInitOverride)
}

func init() {
	flags := configInitCmd.Flags()
	flags.String("name", "",
		"Project name (default: current directory name)")
	flags.String("savepoint-dir", "",
		"Savepoint backup directory (default: .arcave/storage)")
	flags.BoolVar(&configInitOverride, "override", false,
		"Overwrite an existing arcave configuration")

	// --override is transient (never persisted), so it carries no config binding.
	// --savepoint-dir survives as sugar for the directory arm of the storage
	// union: it binds to storage.directory.path, not to a top-level key.
	viperBind(cfgViper, flags, map[string]string{
		"name":          "project.name",
		"savepoint-dir": "storage.directory.path",
	})

	rootCmd.AddCommand(configInitCmd)
}
