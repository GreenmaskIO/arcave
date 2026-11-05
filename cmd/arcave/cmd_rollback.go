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

var rollbackCmd = &cobra.Command{
	Use:   "rollback [savepoint-id|name|latest]",
	Short: "Restore the environment to a specified savepoint",
	Args:  cobra.ExactArgs(1),
	RunE:  runRollback,
}

func runRollback(cmd *cobra.Command, args []string) error {
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
	h := cli.NewRollbackHandler(svc, loadRunOutput())
	return h.Rollback(ctx, projectCfg, args[0])
}

func init() {
	// No flags of its own: the bound it waits for before reverting is
	// compose.wait_timeout, carried by rootCmd's persistent --wait-timeout.
	rootCmd.AddCommand(rollbackCmd)
}
