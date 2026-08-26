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
	"errors"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/cli"
)

var (
	savepointCreateName        string
	savepointCreateDescription string

	savepointCreateCmd = &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new environment savepoint",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runSavepointCreate,
	}
)

func runSavepointCreate(cmd *cobra.Command, args []string) error {
	name, err := savepointCreateResolveName(args)
	if err != nil {
		return err
	}
	if err := requireConfig(); err != nil {
		return err
	}
	return withDeps(cmd, func(ctx context.Context, d *dependencies) error {
		h := cli.NewSavepointCreateHandler(d.service, d.printer)
		return h.SavepointCreate(ctx, projectCfg, name, savepointCreateDescription)
	})
}

// savepointCreateResolveName reconciles the optional positional name with
// --name. Supplying both is only an error when they disagree.
func savepointCreateResolveName(args []string) (string, error) {
	if len(args) == 0 {
		return savepointCreateName, nil
	}
	if savepointCreateName != "" && savepointCreateName != args[0] {
		return "", errors.New("savepoint name given both as argument and --name")
	}
	return args[0], nil
}

func init() {
	flags := savepointCreateCmd.Flags()
	flags.StringVar(&savepointCreateName, "name", "", "Savepoint name")
	flags.StringVar(&savepointCreateDescription, "description", "", "Savepoint description")

	// Neither is bound to viper: --name would collide with `arcave init --name`
	// on the shared "name" config key, conflating the project name with this
	// savepoint's name. Both are per-run values, not project config — unlike
	// --wait-timeout, which is a config key and lives on rootCmd.

	savepointCmd.AddCommand(savepointCreateCmd)
}
