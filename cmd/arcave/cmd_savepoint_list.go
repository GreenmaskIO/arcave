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
	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/cli"
	"github.com/greenmaskio/arcave/internal/model"
)

var (
	savepointListIncludeSafety = false
	savepointListSort          = string(model.SortDesc)

	savepointListCmd = &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List available savepoints",
		Args:    cobra.NoArgs,
		RunE:    runSavepointList,
	}
)

func runSavepointList(cmd *cobra.Command, _ []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	order, err := model.ParseSortOrder(savepointListSort)
	if err != nil {
		return err
	}
	composeProject, err := requireComposeProject()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	var opened closers
	defer opened.release(ctx)

	svc, err := openSavepointStoreService(ctx, projectRoot, projectCfg, sessionRef, composeProject, &opened)
	if err != nil {
		return err
	}
	h := cli.NewSavepointListHandler(svc, loadRunOutput())
	return h.SavepointList(ctx, model.ListRequest{
		IncludeSafetySnapshots: savepointListIncludeSafety,
		Sort:                   order,
	})
}

func init() {
	savepointListCmd.Flags().BoolVar(&savepointListIncludeSafety, "include-safety-snapshots", false,
		"also list the safety snapshots a rollback takes of the live state; rolling back to one by id undoes that rollback")
	savepointListCmd.Flags().StringVar(&savepointListSort, "sort", string(model.SortDesc),
		"order by age: desc (newest first) or asc")
	savepointCmd.AddCommand(savepointListCmd)
}
