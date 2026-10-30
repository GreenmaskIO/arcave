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
	"errors"
	"slices"
	"time"

	"github.com/rs/zerolog"
)

// muteReporter is the reporter of a context without a tracker: every method is a no-op.
var muteReporter = &StepReporter{}

// StepReporter is the handle through which one step reports its progress. Its methods are safe for concurrent use.
type StepReporter struct {
	tracker   *Tracker
	parent    *StepReporter
	step      Step
	measures  []Measure
	batch     bool
	startedAt time.Time
	logger    *zerolog.Logger
	// ctx is the context the step was opened on: a cancellation of it is no failure.
	ctx context.Context

	// The fields below are guarded by tracker.mu.
	id          StepID
	opened      bool
	ended       bool
	childFailed bool
	msg         string
	sample      Sample
}

// Update reports msg as the step's current message; a lazy workflow keeps it unopened.
func (r *StepReporter) Update(msg string) {
	r.report(StatusInProgress, msg, nil, false, false)
}

// UpdateResource reports msg about one resource of a summary activity, still under way.
func (r *StepReporter) UpdateResource(resource Resource, msg string) {
	r.report(StatusInProgress, msg, &resource, false, false)
}

// SettleResource reports msg about one resource of a summary activity that has reached its
// outcome; the step goes on.
func (r *StepReporter) SettleResource(resource Resource, msg string) {
	r.report(StatusInProgress, msg, &resource, true, false)
}

// WarnResource reports a warning about one resource of a summary activity.
func (r *StepReporter) WarnResource(resource Resource, msg string) {
	r.report(StatusWarned, msg, &resource, false, false)
}

// Warn reports a warning; it opens a lazy workflow.
func (r *StepReporter) Warn(msg string) {
	r.report(StatusWarned, msg, nil, false, false)
}

// WarnInResult is Warn for a warning the operation's result repeats, marked InResult.
func (r *StepReporter) WarnInResult(msg string) {
	r.report(StatusWarned, msg, nil, false, true)
}

// IsMute reports whether the step reports nothing: a context without a tracker carries one.
func (r *StepReporter) IsMute() bool {
	return r.isMute()
}

// Progress reports the step's accumulated sample; the fields it leaves nil keep their values.
// Like Update, it does not open a lazy workflow.
func (r *StepReporter) Progress(sample Sample) {
	if r.isMute() {
		return
	}
	r.tracker.mu.Lock()
	defer r.tracker.mu.Unlock()
	if r.ended {
		return
	}
	r.mergeLocked(sample)
	if !r.opened {
		return
	}
	r.emitLocked(Event{
		Status: StatusInProgress,
		Msg:    r.msg,
	})
}

// Done ends the step succeeded, merging sample as Progress does. A lazy workflow never opened
// reports nothing, though a batch above it counts it.
func (r *StepReporter) Done(sample Sample) {
	if r.isMute() {
		return
	}
	r.tracker.mu.Lock()
	defer r.tracker.mu.Unlock()
	if r.ended {
		return
	}
	r.ended = true
	r.mergeLocked(sample)
	if r.opened {
		r.emitLocked(Event{
			Status: StatusSucceeded,
			Msg:    r.msg,
		})
	}
	r.parent.countChildLocked(StatusSucceeded)
}

// Fail ends the step failed, or cancelled when err is a context.Canceled or the cause the
// context it was opened on was cancelled with; a deadline is a failure. A workflow carries err
// only when none of its descendants failed: the error is reported where it arose.
func (r *StepReporter) Fail(msg string, err error) {
	if r.isMute() {
		return
	}
	r.tracker.mu.Lock()
	defer r.tracker.mu.Unlock()
	if r.ended {
		return
	}
	r.ended = true
	r.openLocked()
	if msg != "" {
		r.msg = msg
	}
	var (
		status  = StatusFailed
		errText string
	)
	switch {
	case r.isCancellation(err):
		status = StatusCancelled
	case err != nil && (r.step.Type == StepTypeActivity || !r.childFailed):
		errText = err.Error()
	}
	r.emitLocked(Event{
		Status: status,
		Msg:    r.msg,
		Error:  errText,
	})
	if status == StatusFailed {
		for ancestor := r.parent; ancestor != nil; ancestor = ancestor.parent {
			ancestor.childFailed = true
		}
	}
	r.parent.countChildLocked(status)
}

// End ends the step: Done with no sample for a nil err, Fail otherwise. Deferred, it takes a
// closure over a named error — defer func() { step.End(err) }() — or it reports success.
func (r *StepReporter) End(err error) {
	if err == nil {
		r.Done(Sample{})
		return
	}
	r.Fail("", err)
}

// isCancellation reports whether err is the step's context being cancelled rather than a
// failure: a context.Canceled, or the cause its context was cancelled with.
func (r *StepReporter) isCancellation(err error) bool {
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	return r.ctx.Err() != nil && errors.Is(err, context.Cause(r.ctx))
}

func (r *StepReporter) isMute() bool {
	return r == nil || r.tracker == nil
}

// isEnded reports whether the step has ended.
func (r *StepReporter) isEnded() bool {
	r.tracker.mu.Lock()
	defer r.tracker.mu.Unlock()
	return r.ended
}

func (r *StepReporter) declares(measure Measure) bool {
	return slices.Contains(r.measures, measure)
}

// declaresSample reports whether the step takes every field sample sets from a caller.
func (r *StepReporter) declaresSample(sample Sample) bool {
	return (sample.Bytes == nil || r.declares(MeasureBytes)) &&
		(sample.Items == nil || (r.declares(MeasureItems) && !r.batch))
}

func (r *StepReporter) report(status Status, msg string, resource *Resource, settled, inResult bool) {
	if r.isMute() {
		return
	}
	r.tracker.mu.Lock()
	defer r.tracker.mu.Unlock()
	if r.ended {
		return
	}
	if resource != nil && !r.declares(MeasureResources) {
		r.warnUndeclared(MeasureResources)
		return
	}
	// Only an Update's message is the step's own; a warning's or a resource's is its event's alone.
	if status == StatusInProgress && resource == nil {
		r.msg = msg
	}
	if status == StatusInProgress && !r.opened {
		return
	}
	r.emitLocked(Event{
		Status:   status,
		Msg:      msg,
		Resource: resource,
		Settled:  settled,
		InResult: inResult,
	})
}

// mergeLocked takes the fields sample sets that the step declares, warning of the rest.
func (r *StepReporter) mergeLocked(sample Sample) {
	if sample.Bytes != nil {
		if r.declares(MeasureBytes) {
			r.sample.Bytes = newCopy(sample.Bytes)
		} else {
			r.warnUndeclared(MeasureBytes)
		}
	}
	if sample.Items != nil {
		if r.declares(MeasureItems) && !r.batch {
			r.sample.Items = newCopy(sample.Items)
		} else {
			r.warnUndeclared(MeasureItems)
		}
	}
}

func (r *StepReporter) warnUndeclared(measure Measure) {
	r.logger.Warn().
		Str("kind", string(r.step.Kind)).
		Str("measure", string(measure)).
		Msg("progress: the step does not report this measure, dropped")
}

// countChildLocked counts a child's outcome on a batch and reports the new count.
func (r *StepReporter) countChildLocked(status Status) {
	if r == nil || !r.batch || r.ended {
		return
	}
	items := *r.sample.Items
	switch status {
	case StatusSucceeded:
		items.Done++
	case StatusFailed:
		items.Failed++
	case StatusCancelled:
		items.Cancelled++
	}
	r.sample.Items = &items
	r.emitLocked(Event{
		Status: StatusInProgress,
		Msg:    r.msg,
	})
}

// emitLocked reports evt, opening the step, and its unopened ancestors, first.
func (r *StepReporter) emitLocked(evt Event) {
	r.openLocked()
	r.tracker.renderer.Render(r.newEventLocked(evt))
}

func (r *StepReporter) openLocked() {
	if r.opened {
		return
	}
	var parentID StepID
	if r.parent != nil {
		r.parent.openLocked()
		parentID = r.parent.id
	}
	r.tracker.lastID++
	r.id = r.tracker.lastID
	r.opened = true
	open := Open{
		Parent:   parentID,
		Step:     r.step,
		Measures: r.measures,
	}
	r.tracker.renderer.Render(r.newEventLocked(Event{
		Status: StatusStarted,
		Open:   &open,
		Msg:    r.msg,
	}))
}

// newEventLocked is evt as the step's event now: its id, a copy of its sample, so a renderer may
// keep the event, and the time.
func (r *StepReporter) newEventLocked(evt Event) Event {
	now := r.tracker.now()
	evt.StepID = r.id
	evt.Sample = newSampleCopy(r.sample, now.Sub(r.startedAt))
	evt.At = now
	return evt
}

// newSampleCopy copies sample, pointers included, with elapsed set.
func newSampleCopy(sample Sample, elapsed time.Duration) Sample {
	return Sample{
		Elapsed: elapsed,
		Bytes:   newCopy(sample.Bytes),
		Items:   newCopy(sample.Items),
	}
}

func newCopy[T any](v *T) *T {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}
