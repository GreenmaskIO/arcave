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

// upForce is --force: drop what the daemon holds of the compose project first.
var upForce bool

var upCmd = &cobra.Command{
	Use:   "up [savepoint-id|name|latest]",
	Short: "Bring the environment up from a savepoint, never over a running one",
	Long: `Build the compose project from a savepoint — the newest one when none is named — and start it.
Refused before anything is written when the daemon still holds anything of the compose project:
containers, or volumes it owns, as a ` + "`docker compose down`" + ` without -v leaves them.
--force drops all of it first, with no backup; ` + "`arcave rollback`" + ` replaces a running
environment and keeps a safety snapshot.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runUp,
}

func runUp(cmd *cobra.Command, args []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	composeProject, err := requireComposeProject()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	var opened closers
	defer opened.release(ctx)

	svc, err := openSavepointService(ctx, projectRoot, projectCfg, sessionRef, composeProject, &opened)
	if err != nil {
		return fmt.Errorf("init dependencies: %w", err)
	}
	var ref string
	if len(args) > 0 {
		ref = args[0]
	}
	return cli.NewUpHandler(svc, loadRunOutput()).Up(ctx, projectCfg, ref, upForce)
}

func init() {
	// The health-check bound is compose.wait_timeout, set by rootCmd's persistent --wait-timeout.
	upCmd.Flags().BoolVar(&upForce, "force", false,
		"Drop every container, volume and network of the compose project first, with no backup")
	rootCmd.AddCommand(upCmd)
}
