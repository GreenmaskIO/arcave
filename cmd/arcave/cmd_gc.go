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
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/cli"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/printer/json"
)

var (
	gcDryRun             bool
	gcRestoredLayerGrace time.Duration
	gcDataKinds          []string

	gcCmd = &cobra.Command{
		Use:   "gc",
		Short: "Report the garbage arcave leaves behind",
		Long: `Report what no live savepoint of the session keeps alive, in every compose project it
holds: savepoint data no record commits, deduplicated volumes no savepoint names, image blobs
no stored manifest lists, upstream images no savepoint names, and restored layers no container
runs on. --kind narrows a run. A capture still in progress holds back what it may name.
Nothing is deleted yet: --dry-run is required. The restored layer grace defaults to
gc.restored_layer_grace. A session tree no record commits is 'session list --all''s.`,
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
	var (
		dataKinds []model.GarbageDataKind
		grace     *time.Duration
	)
	for _, dataKind := range gcDataKinds {
		dataKinds = append(dataKinds, model.GarbageDataKind(dataKind))
	}
	if cmd.Flags().Changed("restored-layer-grace") {
		grace = &gcRestoredLayerGrace
	}
	req := model.GarbageCollectRequest{
		DryRun:             gcDryRun,
		DataKinds:          dataKinds,
		RestoredLayerGrace: grace,
	}
	if err := cli.ValidateGarbageCollectRequest(req); err != nil {
		return err
	}
	return withGCDeps(cmd.Context(), func(ctx context.Context, d *gcDependencies) error {
		return cli.NewGCHandler(d.service, json.New(os.Stdout)).GarbageCollect(ctx, req)
	})
}

// withGCDeps runs fn over the gc dependencies, released when fn returns.
func withGCDeps(ctx context.Context, fn func(ctx context.Context, d *gcDependencies) error) error {
	deps, err := newGCDependencies(ctx, projectRoot, projectCfg, sessionRef)
	if err != nil {
		return fmt.Errorf("init gc dependencies: %w", err)
	}
	defer func() {
		if err := deps.Close(ctx); err != nil {
			log.Ctx(ctx).Error().Err(err).Msg("close gc dependencies")
		}
	}()
	return fn(ctx, deps)
}

func init() {
	flags := gcCmd.Flags()
	flags.BoolVar(&gcDryRun, "dry-run", false, "Report the garbage without deleting it (required for now)")
	flags.DurationVar(&gcRestoredLayerGrace, "restored-layer-grace", 0,
		"How old an unused layer tag must be to count as garbage; default gc.restored_layer_grace")
	flags.StringSliceVar(&gcDataKinds, "kind", nil,
		"What to look at, repeatable: savepoint-data, deduplicated-volumes, image-layers, upstream-images, "+
			"restored-layers; default every one")
	rootCmd.AddCommand(gcCmd)
}
