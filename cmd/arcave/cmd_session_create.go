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
	sessionCreateFlagName string
	sessionCreateFlagFrom string

	sessionCreateCmd = &cobra.Command{
		Use:   "create",
		Short: "Create a session, empty or a copy of another",
		Long: `Create a session. With --from it copies every compose project and savepoint of
that session into the new one and touches no container; roll back inside it to deploy.`,
		Args: cobra.NoArgs,
		RunE: runSessionCreate,
	}
)

func runSessionCreate(cmd *cobra.Command, _ []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	return withSessionDeps(cmd.Context(), func(d *sessionDependencies) error {
		return cli.NewSessionCreateHandler(d.service, d.printer).
			SessionCreate(cmd.Context(), model.CreateSessionRequest{
				Name: sessionCreateFlagName,
				From: sessionCreateFlagFrom,
			})
	})
}

func init() {
	// Unbound: it would collide with init --name on the shared name key.
	sessionCreateCmd.Flags().StringVar(&sessionCreateFlagName, "name", "",
		"Session name, unique within the arcave project; --session accepts it in place of the id")
	sessionCreateCmd.Flags().StringVar(&sessionCreateFlagFrom, "from", "",
		"Session to copy, by name or id; the copy costs that session's size")
	sessionCmd.AddCommand(sessionCreateCmd)
}
