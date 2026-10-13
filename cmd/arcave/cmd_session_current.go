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

var sessionCurrentCmd = &cobra.Command{
	Use:   "current",
	Short: "Show the session commands work in",
	Long: "Show the session commands work in: --session or ARCAVE_SESSION when given, else the current " +
		"session (`arcave session set`), else the default. A current session that no longer exists is an error.",
	Args: cobra.NoArgs,
	RunE: runSessionCurrent,
}

func runSessionCurrent(cmd *cobra.Command, _ []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	return withSessionDeps(cmd.Context(), func(d *sessionDependencies) error {
		return cli.NewSessionCurrentHandler(d.service, d.printer).SessionCurrent(cmd.Context(), sessionRef)
	})
}

func init() {
	sessionCmd.AddCommand(sessionCurrentCmd)
}
