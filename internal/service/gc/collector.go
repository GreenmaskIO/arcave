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

package gc

import (
	"context"
	"fmt"
	"slices"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// ObjectCollector finds the garbage of one data kind, one implementation's way.
type ObjectCollector interface {
	// Kind names the collector; DataKind what it resolves.
	Kind() model.GarbageCollectorKind
	DataKind() model.GarbageDataKind
}

// DaemonCollector is a collector judging the daemon, once per gc.
type DaemonCollector interface {
	ObjectCollector
	// Find reports what is dead in the daemon; it only reads.
	Find(ctx context.Context, scope DaemonScope) (model.GarbageStat, error)
}

// ComposeProjectCollector is a collector judging one compose project's store, once per compose
// project of the session.
type ComposeProjectCollector interface {
	ObjectCollector
	// Find reports what is dead in scope; it only reads.
	Find(ctx context.Context, scope ComposeProjectScope) (model.GarbageStat, error)
}

// collectorProbe is a collector reading something besides the store, which may not answer.
type collectorProbe interface {
	Probe(ctx context.Context) error
}

// collectorSet is the collectors one request runs, and why any of them cannot.
type collectorSet struct {
	daemon         []DaemonCollector
	composeProject []ComposeProjectCollector
	// unavailable aborts the collector of a kind whose probe failed.
	unavailable map[model.GarbageCollectorKind]error
}

// readCollectorSet picks, in their order, the collectors resolving dataKinds — none named looks
// at every data kind — and probes them: one that cannot reach what it reads is aborted, since a
// report that looked at nothing would read as clean.
func (s *GarbageCollector) readCollectorSet(ctx context.Context, dataKinds []model.GarbageDataKind) collectorSet {
	var (
		daemon         = selectCollectors(s.daemonCollectors, dataKinds)
		composeProject = selectCollectors(s.composeProjectCollectors, dataKinds)
		unavailable    = map[model.GarbageCollectorKind]error{}
	)
	for _, collector := range slices.Concat(toObjectCollectors(daemon), toObjectCollectors(composeProject)) {
		if err := probeCollector(ctx, collector); err != nil {
			unavailable[collector.Kind()] = err
		}
	}
	return collectorSet{
		daemon:         daemon,
		composeProject: composeProject,
		unavailable:    unavailable,
	}
}

// selectCollectors keeps, in their order, the collectors resolving dataKinds; none named keeps every one.
func selectCollectors[C ObjectCollector](collectors []C, dataKinds []model.GarbageDataKind) []C {
	var selected []C
	for _, collector := range collectors {
		if len(dataKinds) == 0 || slices.Contains(dataKinds, collector.DataKind()) {
			selected = append(selected, collector)
		}
	}
	return selected
}

// toObjectCollectors is collectors as the kind-agnostic interface.
func toObjectCollectors[C ObjectCollector](collectors []C) []ObjectCollector {
	out := make([]ObjectCollector, 0, len(collectors))
	for _, collector := range collectors {
		out = append(out, collector)
	}
	return out
}

// probeCollector probes collector when it reads something besides the store.
func probeCollector(ctx context.Context, collector ObjectCollector) error {
	probe, ok := collector.(collectorProbe)
	if !ok {
		return nil
	}
	err := probe.Probe(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Info().Err(err).Str("collector", string(collector.Kind())).Msg("garbage kind is not judged")
	}
	return err
}

// runDaemonCollectors runs every daemon collector of the set over scope.
func (c *collectorSet) runDaemonCollectors(
	ctx context.Context, scope DaemonScope, scopeErr error,
) []model.GarbageReport {
	var reports []model.GarbageReport
	for _, collector := range c.daemon {
		reports = append(reports, c.runCollector(ctx, collector, "", scopeErr, func() (model.GarbageStat, error) {
			return collector.Find(ctx, scope)
		}))
	}
	return reports
}

// runComposeProjectCollectors runs every compose-project collector of the set over the scope of
// compose project name.
func (c *collectorSet) runComposeProjectCollectors(
	ctx context.Context, name model.DockerComposeProjectName, scope ComposeProjectScope, scopeErr error,
) []model.GarbageReport {
	var reports []model.GarbageReport
	for _, collector := range c.composeProject {
		reports = append(reports, c.runCollector(ctx, collector, name, scopeErr, func() (model.GarbageStat, error) {
			return collector.Find(ctx, scope)
		}))
	}
	return reports
}

// runCollector runs find into the collector's report: a scope or probe failure aborts it, a
// failure of find is the report's own error.
func (c *collectorSet) runCollector(
	ctx context.Context, collector ObjectCollector, composeProject model.DockerComposeProjectName, scopeErr error,
	find func() (model.GarbageStat, error),
) model.GarbageReport {
	_, step := progress.Run(ctx, progress.Step{Kind: kindCollector, ID: string(collector.Kind())})
	report := c.readCollectorReport(ctx, collector, composeProject, scopeErr, find)
	endCollectorStep(ctx, step, report)
	return report
}

// readCollectorReport runs find into the collector's report.
func (c *collectorSet) readCollectorReport(
	ctx context.Context, collector ObjectCollector, composeProject model.DockerComposeProjectName, scopeErr error,
	find func() (model.GarbageStat, error),
) model.GarbageReport {
	var (
		stat    model.GarbageStat
		summary model.GarbageSummary
		failure string
	)
	switch unavailable := c.unavailable[collector.Kind()]; {
	case scopeErr != nil:
		summary = model.GarbageSummary{Aborted: fmt.Sprintf("cannot read the scope: %v", scopeErr)}
	case unavailable != nil:
		summary = model.GarbageSummary{Aborted: unavailable.Error()}
	default:
		if stat, failure = runFind(ctx, collector, composeProject, find); stat != nil {
			summary = stat.Summary()
		}
	}
	return model.GarbageReport{
		DataKind:       collector.DataKind(),
		Collector:      collector.Kind(),
		ComposeProject: composeProject,
		Summary:        summary,
		Detail:         stat,
		Error:          failure,
	}
}

// runFind runs find; a failure, or no finding, is logged and returned as the report's error.
func runFind(
	ctx context.Context, collector ObjectCollector, composeProject model.DockerComposeProjectName,
	find func() (model.GarbageStat, error),
) (model.GarbageStat, string) {
	stat, err := find()
	if err == nil && stat == nil {
		err = fmt.Errorf("collector %s returned no finding", collector.Kind())
	}
	if err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("collector", string(collector.Kind())).
			Str("compose_project", composeProject.String()).Msg("garbage collector failed")
		return nil, err.Error()
	}
	return stat, ""
}
