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

	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/cli"
	"github.com/greenmaskio/arcave/internal/model"
)

var (
	savepointRemoveRetainRecent int
	savepointRemoveRetainFor    string
	savepointRemoveBeforeDate   string
	savepointRemoveDryRun       bool

	savepointRemoveCmd = &cobra.Command{
		Use:     "remove [id...]",
		Aliases: []string{"rm"},
		Short:   "Remove savepoints by ID, or by retention rules",
		Args:    cobra.ArbitraryArgs,
		RunE:    runSavepointRemove,
	}
)

func runSavepointRemove(cmd *cobra.Command, args []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	return withDeps(cmd, func(ctx context.Context, d *dependencies) error {
		h := cli.NewSavepointRemoveHandler(d.service, d.printer)
		return h.SavepointRemove(ctx, model.RemoveRequest{
			IDs:          model.SavepointIDs(args),
			RetainRecent: savepointRemoveRetainRecent,
			RetainFor:    savepointRemoveRetainFor,
			BeforeDate:   savepointRemoveBeforeDate,
			DryRun:       savepointRemoveDryRun,
		})
	})
}

func init() {
	flags := savepointRemoveCmd.Flags()
	flags.IntVar(&savepointRemoveRetainRecent, "retain-recent", 0,
		"Retain the most recent N savepoints")
	flags.StringVar(&savepointRemoveRetainFor, "retain-for", "",
		"Retain savepoints for the specified duration (e.g. 1w2d3h4m5s)")
	flags.StringVar(&savepointRemoveBeforeDate, "before-date", "",
		"Delete savepoints older than RFC3339Nano timestamp")
	flags.BoolVar(&savepointRemoveDryRun, "dry-run", false,
		"Show savepoints that would be deleted without deleting them")

	savepointCmd.AddCommand(savepointRemoveCmd)
}
