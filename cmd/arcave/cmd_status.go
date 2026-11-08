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
	"github.com/greenmaskio/arcave/internal/model"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show what a rollback deployed and whether it is running",
	Long: `Show what the daemon runs for the compose project: the savepoint a rollback deployed, from
which session and when, every replica's state, health and published ports, and a rollback in
progress or interrupted. It takes no lock and writes nothing. A store that cannot be read leaves
the daemon's part whole and exits 1; an unhealthy compose project exits 0.`,
	Args: cobra.NoArgs,
	RunE: runStatus,
}

func runStatus(cmd *cobra.Command, _ []string) error {
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

	svc, err := openStatusService(ctx, projectRoot, projectCfg, composeProject, &opened)
	if err != nil {
		return fmt.Errorf("init status dependencies: %w", err)
	}
	return cli.NewStatusHandler(svc, loadRunOutput()).Status(ctx, model.StatusRequest{Session: sessionRef})
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
