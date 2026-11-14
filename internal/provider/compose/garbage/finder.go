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

// Package garbage finds a session's garbage and deletes it: Finder runs the collectors over each
// compose project's scope, and the GarbageEraser of each op's action deletes what they found.
package garbage

import (
	"context"
	"errors"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// errNoEraser fails the ops of a report no one eraser carries out.
var errNoEraser = errors.New("no eraser carries out the report's ops")

// SavepointReader reads the committed savepoints of one compose project's store.
type SavepointReader interface {
	LoadSavepoints(ctx context.Context, ids []model.SavepointID) ([]model.Savepoint, error)
}

// Deps are Finder's collaborators.
type Deps struct {
	// NewSavepointReader opens the savepoints of a compose project's store.
	NewSavepointReader func(store storages.Storager) SavepointReader
	// DaemonCollectors and ComposeProjectCollectors are one per kind, in the order they run.
	DaemonCollectors         []DaemonCollector
	ComposeProjectCollectors []ComposeProjectCollector
	// DaemonErasers and ComposeProjectErasers are one per action at their level.
	DaemonErasers         []DaemonEraser
	ComposeProjectErasers []ComposeProjectEraser
	// UnlistedNamespaces are the top-level directories of a compose project store its scope's
	// listing leaves out: a backend's own, which its collector reads through the backend.
	UnlistedNamespaces []string
}

// Finder finds one session's garbage, and hands each finding back with its eraser.
type Finder struct {
	newSavepointReader       func(store storages.Storager) SavepointReader
	daemonCollectors         []DaemonCollector
	composeProjectCollectors []ComposeProjectCollector
	erasers                  eraserSet
	unlisted                 []string
}

// New builds a Finder over deps.
func New(deps Deps) *Finder {
	return &Finder{
		newSavepointReader:       deps.NewSavepointReader,
		daemonCollectors:         deps.DaemonCollectors,
		composeProjectCollectors: deps.ComposeProjectCollectors,
		erasers:                  newEraserSet(deps.DaemonErasers, deps.ComposeProjectErasers),
		unlisted:                 deps.UnlistedNamespaces,
	}
}

// found is one report and the eraser of its ops; none when no one eraser carries them out.
type found struct {
	report model.GarbageReport
	action model.GarbageAction
	erase  func(ctx context.Context, ops []model.GarbageOp) model.GarbageDeletion
}

// newFound is report with no eraser.
func newFound(report model.GarbageReport) found {
	return found{report: report}
}

// newErasableFound is report whose ops erase carries out, every one of action.
func newErasableFound(
	report model.GarbageReport, action model.GarbageAction,
	erase func(ctx context.Context, ops []model.GarbageOp) model.GarbageDeletion,
) found {
	return found{
		report: report,
		action: action,
		erase:  erase,
	}
}

// Report is what the collector found.
func (f found) Report() model.GarbageReport {
	return f.report
}

// Delete carries out the report's ops, under a step of its compose project, if any, and one of its
// eraser; with no eraser every op fails.
func (f found) Delete(ctx context.Context) model.GarbageDeletion {
	ops := findingOps(f.report)
	switch {
	case len(ops) == 0:
		return model.GarbageDeletion{}
	case f.erase == nil:
		zerolog.Ctx(ctx).Warn().Err(errNoEraser).Str("data_kind", string(f.report.DataKind)).Msg("garbage not deleted")
		return newUnerasedDeletion(ops)
	}
	if f.report.ComposeProject == "" {
		return f.runEraser(ctx, ops)
	}
	projectCtx, step := progress.Begin(ctx, progress.Step{
		Kind: kindComposeProject,
		Name: f.report.ComposeProject.String(),
	})
	deletion := f.runEraser(projectCtx, ops)
	step.End(newDeletionError(projectCtx, &deletion))
	return deletion
}

// runEraser carries out ops under the eraser's step.
func (f found) runEraser(ctx context.Context, ops []model.GarbageOp) model.GarbageDeletion {
	eraseCtx, step := progress.Begin(ctx, progress.Step{
		Kind: kindEraser,
		ID:   string(f.action),
	})
	deletion := f.erase(eraseCtx, ops)
	step.End(newDeletionError(eraseCtx, &deletion))
	zerolog.Ctx(ctx).Debug().Str("data_kind", string(f.report.DataKind)).
		Str("compose_project", f.report.ComposeProject.String()).
		Int("deleted", len(deletion.Deleted)).Int("failed", len(deletion.Failed)).Int("not_run", deletion.NotRun).
		Msg("garbage deleted")
	return deletion
}

// Find runs the collectors resolving dataKinds over session in the arcave project's store — none
// named runs every one — the daemon's first, then each compose project's; it changes nothing. A
// scope it cannot read aborts its reports.
func (s *Finder) Find(
	ctx context.Context, store storages.Navigator, session model.SessionID, dataKinds []model.GarbageDataKind,
) []provider.Garbage {
	var (
		collectors                         = s.readCollectorSet(ctx, dataKinds)
		sessionStore, composeProjects, err = readComposeProjects(ctx, store, session)
		// The daemon's objects are judged against the compose projects: none read is no verdict.
		daemonReports = collectors.runDaemonCollectors(ctx, DaemonScope{ComposeProjects: composeProjects}, err)
		findings      = make([]provider.Garbage, 0, len(daemonReports))
	)
	for _, report := range daemonReports {
		findings = append(findings, s.newDaemonGarbage(report))
	}
	return append(findings, s.findComposeProjectGarbage(ctx, collectors, sessionStore, composeProjects, err)...)
}

// findComposeProjectGarbage runs the compose-project collectors over every compose project.
func (s *Finder) findComposeProjectGarbage(
	ctx context.Context, collectors collectorSet, sessionStore storages.Navigator,
	names []model.DockerComposeProjectName, listErr error,
) []provider.Garbage {
	if len(collectors.composeProject) == 0 {
		// No compose-project collector runs: reading the scopes would only hold the lock longer.
		return nil
	}
	if listErr != nil {
		return s.newComposeProjectGarbage(nil, collectors.runComposeProjectCollectors(ctx, "", ComposeProjectScope{}, listErr))
	}
	var findings []provider.Garbage
	for _, name := range names {
		projectCtx, step := progress.Begin(ctx, progress.Step{
			Kind: kindComposeProject,
			Name: name.String(),
		})
		scope, store, err := s.readComposeProjectScope(projectCtx, sessionStore, name)
		reports := collectors.runComposeProjectCollectors(projectCtx, name, scope, err)
		step.End(context.Cause(projectCtx))
		findings = append(findings, s.newComposeProjectGarbage(store, reports)...)
	}
	return findings
}

// newDaemonGarbage is report with the daemon eraser of its ops.
func (s *Finder) newDaemonGarbage(report model.GarbageReport) found {
	eraser, ok := findEraser(s.erasers.daemon, findingOps(report))
	if !ok {
		return newFound(report)
	}
	return newErasableFound(report, eraser.Action(), func(ctx context.Context, ops []model.GarbageOp) model.GarbageDeletion {
		return eraser.Erase(ctx, ops)
	})
}

// newComposeProjectGarbage is each of reports with the compose-project eraser of its ops, deleting
// in store.
func (s *Finder) newComposeProjectGarbage(store storages.Storager, reports []model.GarbageReport) []provider.Garbage {
	findings := make([]provider.Garbage, 0, len(reports))
	for _, report := range reports {
		eraser, ok := findEraser(s.erasers.composeProject, findingOps(report))
		if !ok {
			findings = append(findings, newFound(report))
			continue
		}
		findings = append(findings, newErasableFound(report, eraser.Action(),
			func(ctx context.Context, ops []model.GarbageOp) model.GarbageDeletion {
				return eraser.Erase(ctx, store, ops)
			}))
	}
	return findings
}

// findingOps are the ops report found; none for a report that found nothing.
func findingOps(report model.GarbageReport) []model.GarbageOp {
	if report.Detail == nil {
		return nil
	}
	return report.Detail.Findings().Ops
}

// newUnerasedDeletion fails every op of ops for want of an eraser.
func newUnerasedDeletion(ops []model.GarbageOp) model.GarbageDeletion {
	failed := make([]model.GarbageDeleteFailure, 0, len(ops))
	for _, op := range ops {
		failed = append(failed, model.GarbageDeleteFailure{
			Target: op.Target,
			Error:  errNoEraser.Error(),
		})
	}
	return model.GarbageDeletion{Failed: failed}
}
