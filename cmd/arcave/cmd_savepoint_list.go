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
	savepointListFlagIncludeSafetySnapshots = false

	savepointListCmd = &cobra.Command{
		Use:   "list",
		Short: "List available savepoints",
		Args:  cobra.NoArgs,
		RunE:  runSavepointList,
	}
)

func runSavepointList(cmd *cobra.Command, _ []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	return withDeps(cmd, func(ctx context.Context, d *dependencies) error {
		h := cli.NewSavepointListHandler(d.service, d.printer)
		return h.SavepointList(ctx, model.ListRequest{
			IncludeSafetySnapshots: savepointListFlagIncludeSafetySnapshots,
		})
	})
}

func init() {
	savepointListCmd.Flags().BoolVar(&savepointListFlagIncludeSafetySnapshots, "include-safety-snapshots", false,
		"also list the safety snapshots a rollback takes of the live state; one that outlived its rollback can be rolled back to by id")
	savepointCmd.AddCommand(savepointListCmd)
}
