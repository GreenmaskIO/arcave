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

var sessionSetCmd = &cobra.Command{
	Use:   "set <id|name>",
	Short: "Make a session the current one",
	Long: "Make a session the current one: every command given no --session works in it, and so does " +
		"`arcave mcp` started without one. The pointer is .arcave/current_session, local to this host.",
	Args: cobra.ExactArgs(1),
	RunE: runSessionSet,
}

func runSessionSet(cmd *cobra.Command, args []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	return withSessionDeps(cmd.Context(), func(d *sessionDependencies) error {
		return cli.NewSessionSetHandler(d.service, d.printer).SessionSet(cmd.Context(), args[0])
	})
}

func init() {
	sessionCmd.AddCommand(sessionSetCmd)
}
