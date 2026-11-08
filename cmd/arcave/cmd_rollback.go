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
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/cli"
	"github.com/greenmaskio/arcave/internal/utils"
)

// errSwitchTakesNoValue refuses a value on a flag that is a plain switch.
var errSwitchTakesNoValue = errors.New("the flag takes no value")

// rollbackSafetySnapshot is what --safety-snapshot or --no-safety-snapshot chose; nil, neither was
// passed and rollback.safety_snapshot decides.
var rollbackSafetySnapshot *bool

var rollbackCmd = &cobra.Command{
	Use:   "rollback [savepoint-id|name|latest]",
	Short: "Restore the environment to a specified savepoint",
	Long: `Restore the environment to a savepoint, capturing the live state first as a safety snapshot:
rolling back to its id undoes the rollback. Each rollback leaves one, and only a run of
` + "`arcave gc`" + ` retires them, past gc.safety_snapshots.retain_recent: run it periodically, or right
after (arcave rollback … && arcave gc).`,
	Args: cobra.ExactArgs(1),
	RunE: runRollback,
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
	cfg := projectCfg
	if rollbackSafetySnapshot != nil {
		cfg.Rollback.SafetySnapshot = rollbackSafetySnapshot
	}
	h := cli.NewRollbackHandler(svc, loadRunOutput())
	return h.Rollback(ctx, cfg, args[0])
}

func init() {
	// The wait before a revert is compose.wait_timeout, set by rootCmd's persistent --wait-timeout.
	flags := rollbackCmd.Flags()
	flags.BoolFunc("safety-snapshot",
		"Capture the live volumes and writable layers first, so the rollback can be undone. Without either flag, rollback.safety_snapshot decides (true unless set)",
		newRollbackSafetySnapshotSwitch(true))
	flags.BoolFunc("no-safety-snapshot",
		"Capture neither the live volumes nor the writable layers: faster, but nothing can undo the rollback, and after a failure the same rollback must be run again",
		newRollbackSafetySnapshotSwitch(false))
	rollbackCmd.MarkFlagsMutuallyExclusive("safety-snapshot", "no-safety-snapshot")
	rootCmd.AddCommand(rollbackCmd)
}

// newRollbackSafetySnapshotSwitch returns the setter of a safety-snapshot switch, refusing any value.
func newRollbackSafetySnapshotSwitch(take bool) func(string) error {
	return func(value string) error {
		if value != "true" {
			return errSwitchTakesNoValue
		}
		rollbackSafetySnapshot = utils.New(take)
		return nil
	}
}
