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
	sessionRemoveForce bool

	sessionRemoveCmd = &cobra.Command{
		Use:     "remove <id|name>",
		Aliases: []string{"rm"},
		Short:   "Remove a session and every savepoint it holds",
		Long: "Remove a session and every savepoint it holds. A compose project running from it is " +
			"left as it is. The default session cannot be removed. A tree without a record — a failed " +
			"or incomplete fork, as `session list --all` shows it — is removed by its id; one still " +
			"cloning needs --force. So does the current session, and removing it clears the pointer: " +
			"commands then work in the default session.",
		Args: cobra.ExactArgs(1),
		RunE: runSessionRemove,
	}
)

func runSessionRemove(cmd *cobra.Command, args []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	return withSessionDeps(cmd.Context(), func(d *sessionDependencies) error {
		return cli.NewSessionRemoveHandler(d.service, d.printer).SessionRemove(cmd.Context(), model.RemoveSessionRequest{Session: args[0], Force: sessionRemoveForce})
	})
}

func init() {
	sessionRemoveCmd.Flags().BoolVar(&sessionRemoveForce, "force", false,
		"Remove the current session, clearing the pointer, or a tree whose fork still looks alive; no fork may be running")
	sessionCmd.AddCommand(sessionRemoveCmd)
}
