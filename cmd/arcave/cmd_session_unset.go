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

var sessionUnsetCmd = &cobra.Command{
	Use:   "unset",
	Short: "Clear the current session, so commands work in the default one",
	Long: "Clear the current session: commands given no --session work in the default session again. " +
		"It is also the way out of a current session that no longer exists.",
	Args: cobra.NoArgs,
	RunE: runSessionUnset,
}

func runSessionUnset(cmd *cobra.Command, _ []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	return withSessionDeps(cmd.Context(), func(d *sessionDependencies) error {
		return cli.NewSessionUnsetHandler(d.service, d.printer).SessionUnset(cmd.Context())
	})
}

func init() {
	sessionCmd.AddCommand(sessionUnsetCmd)
}
