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

// downForce is --force: take the environment down whoever deployed it.
var downForce bool

var downCmd = &cobra.Command{
	Use:   "down",
	Short: "End the environment on the daemon",
	Long: `Take everything of the compose project off the daemon: containers, networks, the volumes it
owns, its containers' anonymous volumes and its restored layer tags. Bind mounts and external
volumes stay. What changed since the last savepoint is lost, and so are the volumes a savepoint
leaves out: take one with ` + "`arcave savepoint create`" + ` first.

Refused when a replica was not built by a rollback of this session — another session's, or
` + "`docker compose up`" + `'s own — and while a rollback's inflight marker stands. --force takes it
down all the same and, for this compose project's marker, clears it.`,
	Args: cobra.NoArgs,
	RunE: runDown,
}

func runDown(cmd *cobra.Command, _ []string) error {
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

	svc, err := openDownService(ctx, projectRoot, projectCfg, sessionRef, composeProject, &opened)
	if err != nil {
		return fmt.Errorf("init dependencies: %w", err)
	}
	return cli.NewDownHandler(svc, loadRunOutput()).Down(ctx, downForce)
}

func init() {
	downCmd.Flags().BoolVar(&downForce, "force", false,
		"Take the environment down whoever deployed it, and clear this compose project's inflight marker")
	rootCmd.AddCommand(downCmd)
}
