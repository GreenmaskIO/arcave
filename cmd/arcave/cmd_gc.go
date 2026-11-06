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
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/cli"
	"github.com/greenmaskio/arcave/internal/model"
)

var (
	gcDryRun    bool
	gcDataKinds []string

	gcCmd = &cobra.Command{
		Use:   "gc",
		Short: "Find and delete the garbage arcave leaves behind",
		Long: `Find and delete what no live savepoint of the session keeps alive, in every compose project
it holds, and the restored layer tags no container uses. The layers of an image a run deletes go
on the next run. A removed savepoint's restic snapshots are forgotten and their repository pruned.`,
		Args: cobra.NoArgs,
		RunE: runGC,
	}
)

func runGC(cmd *cobra.Command, _ []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	// The environment's ARCAVE_PROJECT_NAME is kept for the savepoint commands and ignored here.
	if cmd.Flags().Changed("project-name") {
		return errors.New("gc works on a session, every compose project in it: drop -p/--project-name")
	}
	var dataKinds []model.GarbageDataKind
	for _, dataKind := range gcDataKinds {
		dataKinds = append(dataKinds, model.GarbageDataKind(dataKind))
	}
	req := model.GarbageCollectRequest{
		DryRun:    gcDryRun,
		DataKinds: dataKinds,
	}
	if err := cli.ValidateGarbageCollectRequest(req); err != nil {
		return err
	}
	ctx := cmd.Context()
	var opened closers
	defer opened.release(ctx)

	svc, err := openGCService(ctx, projectRoot, projectCfg, sessionRef, &opened)
	if err != nil {
		return fmt.Errorf("init gc dependencies: %w", err)
	}
	return cli.NewGCHandler(svc, loadRunOutput()).GarbageCollect(ctx, req)
}

func init() {
	flags := gcCmd.Flags()
	flags.BoolVar(&gcDryRun, "dry-run", false, "Report the garbage without deleting it")
	flags.StringSliceVar(&gcDataKinds, "kind", nil,
		"What to look at, repeatable: savepoint-data, deduplicated-volumes, image-layers, upstream-images, "+
			"restored-layers; default every one")
	rootCmd.AddCommand(gcCmd)
}
