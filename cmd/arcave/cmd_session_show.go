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
	"cmp"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/cli"
	"github.com/greenmaskio/arcave/internal/model"
)

var (
	sessionShowIncludeSafety bool

	sessionShowCmd = &cobra.Command{
		Use:   "show [id|name]",
		Short: "Show a session and its savepoints, by compose project",
		Long: `Show a session — the one named, else --session or ARCAVE_SESSION, else the current session,
else the default — and its committed savepoints, grouped by compose project: compose projects by name,
savepoints newest first. Safety snapshots are hidden unless --include-safety-snapshots. Reads the
store alone: no daemon, no -p.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runSessionShow,
	}
)

func runSessionShow(cmd *cobra.Command, args []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	ctx := cmd.Context()
	var opened closers
	defer opened.release(ctx)

	svc, err := openSessionService(ctx, projectRoot, projectCfg, &opened)
	if err != nil {
		return fmt.Errorf("init session dependencies: %w", err)
	}
	// The argument outranks --session, as session set's and session remove's do.
	var named string
	if len(args) > 0 {
		named = args[0]
	}
	return cli.NewSessionShowHandler(svc, loadRunOutput()).SessionShow(ctx,
		model.ShowSessionRequest{
			Session:                cmp.Or(named, sessionRef),
			Ref:                    sessionRef,
			IncludeSafetySnapshots: sessionShowIncludeSafety,
		})
}

func init() {
	sessionShowCmd.Flags().BoolVar(&sessionShowIncludeSafety, "include-safety-snapshots", false,
		"also show the safety snapshots a rollback takes of the live state")
	sessionCmd.AddCommand(sessionShowCmd)
}
