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
	sessionListAll  bool
	sessionListSort = string(model.SortDesc)

	sessionListCmd = &cobra.Command{
		Use:   "list",
		Short: "List the sessions of the arcave project",
		Long: `List the sessions of the arcave project, each with its status. --all adds the trees
without a record: a fork still cloning, one that failed, or one left incomplete.`,
		Args: cobra.NoArgs,
		RunE: runSessionList,
	}
)

func runSessionList(cmd *cobra.Command, _ []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	order, err := model.ParseSortOrder(sessionListSort)
	if err != nil {
		return err
	}
	return withSessionDeps(cmd.Context(), func(d *sessionDependencies) error {
		return cli.NewSessionListHandler(d.service, d.printer).SessionList(cmd.Context(),
			model.ListSessionsRequest{All: sessionListAll, Ref: sessionRef, Sort: order})
	})
}

func init() {
	sessionListCmd.Flags().BoolVar(&sessionListAll, "all", false,
		"Also list the trees without a record: forks cloning, failed or left incomplete")
	sessionListCmd.Flags().StringVar(&sessionListSort, "sort", string(model.SortDesc),
		"order by age: desc (newest first) or asc")
	sessionCmd.AddCommand(sessionListCmd)
}
