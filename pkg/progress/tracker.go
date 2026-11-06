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

package progress

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// defaultSampleInterval is how often a Sampler sends at most.
const defaultSampleInterval = 200 * time.Millisecond

// Renderer renders a progress stream. A Tracker calls it under its own lock, one event at a time.
type Renderer interface {
	Render(evt Event)
}

type (
	stepKey    struct{}
	trackerKey struct{}
)

// Tracker numbers the steps of one operation and hands their events to a Renderer.
type Tracker struct {
	renderer Renderer
	now      func() time.Time
	interval time.Duration

	mu     sync.Mutex
	lastID StepID
}

// New returns a Tracker rendering to renderer, timing on now.
func New(renderer Renderer, now func() time.Time) *Tracker {
	return &Tracker{
		renderer: renderer,
		now:      now,
		interval: defaultSampleInterval,
	}
}

// Discard returns a Tracker whose every step is mute.
func Discard() *Tracker {
	return nil
}

// Begin opens the root workflow, lazily, whatever step ctx carries.
func (t *Tracker) Begin(ctx context.Context, step Step) (context.Context, *StepReporter) {
	if t == nil {
		return withStep(ctx, muteReporter), muteReporter
	}
	reporter := t.newStepReporter(ctx, nil, step, StepTypeWorkflow, nil, Sample{})
	return withStep(ctx, reporter), reporter
}

// WithTracker returns ctx carrying t: a Begin on it with no step opens t's root.
func WithTracker(ctx context.Context, t *Tracker) context.Context {
	return context.WithValue(ctx, trackerKey{}, t)
}

// Begin opens a workflow under the step of ctx, or the root of the tracker ctx carries when it
// carries no step. It reports nothing until its first child or Warn.
func Begin(ctx context.Context, step Step) (context.Context, *StepReporter) {
	if _, ok := ctx.Value(stepKey{}).(*StepReporter); !ok {
		if tracker, ok := ctx.Value(trackerKey{}).(*Tracker); ok {
			return tracker.Begin(ctx, step)
		}
	}
	parent, ok := findParent(ctx, step)
	if !ok {
		return ctx, muteReporter
	}
	reporter := parent.tracker.newStepReporter(ctx, parent, step, StepTypeWorkflow, nil, Sample{})
	return withStep(ctx, reporter), reporter
}

// Batch opens a loop over total objects under the step of ctx, at once; it counts its
// children's outcomes itself. A total of 0 is unknown.
func Batch(ctx context.Context, step Step, total int) (context.Context, *StepReporter) {
	parent, ok := findParent(ctx, step)
	if !ok {
		return ctx, muteReporter
	}
	var (
		measures = []Measure{MeasureItems}
		sample   = Sample{Items: &Items{Total: int64(total)}}
		reporter = parent.tracker.newStepReporter(ctx, parent, step, StepTypeWorkflow, measures, sample)
	)
	reporter.tracker.mu.Lock()
	defer reporter.tracker.mu.Unlock()
	reporter.openLocked()
	return withStep(ctx, reporter), reporter
}

// Run opens an activity under the step of ctx, at once, reporting the measures it declares.
func Run(ctx context.Context, step Step, measures ...Measure) (context.Context, *StepReporter) {
	parent, ok := findParent(ctx, step)
	if !ok {
		return ctx, muteReporter
	}
	reporter := parent.tracker.newStepReporter(ctx, parent, step, StepTypeActivity, measures, Sample{})
	reporter.tracker.mu.Lock()
	defer reporter.tracker.mu.Unlock()
	reporter.openLocked()
	return withStep(ctx, reporter), reporter
}

// WithStep returns ctx carrying reporter: the work that follows an activity, on a context derived
// from the activity's, goes on under reporter again.
func WithStep(ctx context.Context, reporter *StepReporter) context.Context {
	return withStep(ctx, reporter)
}

// Current returns the step of ctx, a mute one when ctx carries none.
func Current(ctx context.Context) *StepReporter {
	if reporter, ok := ctx.Value(stepKey{}).(*StepReporter); ok {
		return reporter
	}
	return muteReporter
}

func (t *Tracker) newStepReporter(
	ctx context.Context,
	parent *StepReporter,
	step Step,
	stepType StepType,
	measures []Measure,
	sample Sample,
) *StepReporter {
	step.Type = stepType
	return &StepReporter{
		tracker:   t,
		parent:    parent,
		step:      step,
		measures:  measures,
		batch:     sample.Items != nil,
		sample:    sample,
		startedAt: t.now(),
		logger:    zerolog.Ctx(ctx),
		ctx:       ctx,
	}
}

func withStep(ctx context.Context, reporter *StepReporter) context.Context {
	return context.WithValue(ctx, stepKey{}, reporter)
}

// findParent returns the step of ctx a child may open under: neither mute, an activity nor ended.
func findParent(ctx context.Context, step Step) (*StepReporter, bool) {
	parent := Current(ctx)
	if parent.isMute() {
		return nil, false
	}
	if parent.step.Type == StepTypeActivity {
		zerolog.Ctx(ctx).Warn().
			Str("kind", string(step.Kind)).
			Str("parent", string(parent.step.Kind)).
			Msg("progress: an activity has no children, the step is mute")
		return nil, false
	}
	if parent.isEnded() {
		zerolog.Ctx(ctx).Warn().
			Str("kind", string(step.Kind)).
			Str("parent", string(parent.step.Kind)).
			Msg("progress: the parent has ended, the step is mute")
		return nil, false
	}
	return parent, true
}
