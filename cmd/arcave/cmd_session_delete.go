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
)

var sessionDeleteCmd = &cobra.Command{
	Use:   "delete <id|name>",
	Short: "Delete a session and every savepoint it holds",
	Long: "Delete a session and every savepoint it holds. A compose project running from it is " +
		"left as it is. The default session cannot be deleted.",
	Args: cobra.ExactArgs(1),
	RunE: runSessionDelete,
}

func runSessionDelete(cmd *cobra.Command, args []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	return withSessionDeps(cmd.Context(), func(d *sessionDependencies) error {
		return cli.NewSessionDeleteHandler(d.service, d.printer).SessionDelete(cmd.Context(), args[0])
	})
}

func init() {
	sessionCmd.AddCommand(sessionDeleteCmd)
}
