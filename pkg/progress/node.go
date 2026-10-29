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

// muteNode is the node of a context without a tracker: every method is a no-op.
var muteNode = &Node{}

// Node is the handle of one node of the tree. Its methods are safe for concurrent use.
type Node struct {
	tracker   *Tracker
	parent    *Node
	step      Step
	measures  []Measure
	batch     bool
	startedAt time.Time
	logger    *zerolog.Logger
	// ctx is the context the node was opened on: a cancellation of it is no failure.
	ctx context.Context

	// The fields below are guarded by tracker.mu.
	id          NodeID
	opened      bool
	ended       bool
	childFailed bool
	msg         string
	sample      Sample
}

// Update reports msg as the node's current message; a lazy workflow keeps it unopened.
func (n *Node) Update(msg string) {
	n.report(StatusInProgress, msg, nil, false, false)
}

// UpdateResource reports msg about one resource of a summary activity, still under way.
func (n *Node) UpdateResource(resource Resource, msg string) {
	n.report(StatusInProgress, msg, &resource, false, false)
}

// SettleResource reports msg about one resource of a summary activity that has reached its
// outcome; the node goes on.
func (n *Node) SettleResource(resource Resource, msg string) {
	n.report(StatusInProgress, msg, &resource, true, false)
}

// WarnResource reports a warning about one resource of a summary activity.
func (n *Node) WarnResource(resource Resource, msg string) {
	n.report(StatusWarned, msg, &resource, false, false)
}

// Warn reports a warning; it opens a lazy workflow.
func (n *Node) Warn(msg string) {
	n.report(StatusWarned, msg, nil, false, false)
}

// WarnInResult is Warn for a warning the operation's result repeats, marked InResult.
func (n *Node) WarnInResult(msg string) {
	n.report(StatusWarned, msg, nil, false, true)
}

// IsMute reports whether the node reports nothing: a context without a tracker carries one.
func (n *Node) IsMute() bool {
	return n.isMute()
}

// Progress reports the node's accumulated sample; the fields it leaves nil keep their values.
// Like Update, it does not open a lazy workflow.
func (n *Node) Progress(sample Sample) {
	if n.isMute() {
		return
	}
	n.tracker.mu.Lock()
	defer n.tracker.mu.Unlock()
	if n.ended {
		return
	}
	n.mergeLocked(sample)
	if !n.opened {
		return
	}
	n.emitLocked(StatusInProgress, nil, false, false, "")
}

// Done ends the node succeeded, merging sample as Progress does. A lazy workflow never opened
// reports nothing, though a batch above it counts it.
func (n *Node) Done(sample Sample) {
	if n.isMute() {
		return
	}
	n.tracker.mu.Lock()
	defer n.tracker.mu.Unlock()
	if n.ended {
		return
	}
	n.ended = true
	n.mergeLocked(sample)
	if n.opened {
		n.emitLocked(StatusSucceeded, nil, false, false, "")
	}
	n.parent.countChildLocked(StatusSucceeded)
}

// Fail ends the node failed, or cancelled when err is a context.Canceled or the cause the
// context it was opened on was cancelled with; a deadline is a failure. A workflow carries err
// only when none of its descendants failed: the error is reported where it arose.
func (n *Node) Fail(msg string, err error) {
	if n.isMute() {
		return
	}
	n.tracker.mu.Lock()
	defer n.tracker.mu.Unlock()
	if n.ended {
		return
	}
	n.ended = true
	n.openLocked()
	if msg != "" {
		n.msg = msg
	}
	var (
		status  = StatusFailed
		errText string
	)
	switch {
	case n.isCancellation(err):
		status = StatusCancelled
	case err != nil && (n.step.Type == NodeTypeActivity || !n.childFailed):
		errText = err.Error()
	}
	n.emitLocked(status, nil, false, false, errText)
	if status == StatusFailed {
		for ancestor := n.parent; ancestor != nil; ancestor = ancestor.parent {
			ancestor.childFailed = true
		}
	}
	n.parent.countChildLocked(status)
}

// End ends the node: Done with no sample for a nil err, Fail otherwise. Deferred, it takes a
// closure over a named error — defer func() { n.End(err) }() — or it reports success.
func (n *Node) End(err error) {
	if err == nil {
		n.Done(Sample{})
		return
	}
	n.Fail("", err)
}

// isCancellation reports whether err is the node's context being cancelled rather than a
// failure: a context.Canceled, or the cause its context was cancelled with.
func (n *Node) isCancellation(err error) bool {
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	return n.ctx.Err() != nil && errors.Is(err, context.Cause(n.ctx))
}

func (n *Node) isMute() bool {
	return n == nil || n.tracker == nil
}

func (n *Node) declares(measure Measure) bool {
	return slices.Contains(n.measures, measure)
}

// declaresSample reports whether the node takes every field sample sets from a caller.
func (n *Node) declaresSample(sample Sample) bool {
	return (sample.Bytes == nil || n.declares(MeasureBytes)) &&
		(sample.Items == nil || (n.declares(MeasureItems) && !n.batch))
}

func (n *Node) report(status Status, msg string, resource *Resource, settled, inResult bool) {
	if n.isMute() {
		return
	}
	n.tracker.mu.Lock()
	defer n.tracker.mu.Unlock()
	if n.ended {
		return
	}
	if status == StatusInProgress && !n.opened {
		n.msg = msg
		return
	}
	n.openLocked()
	n.msg = msg
	if resource != nil && !n.declares(MeasureResources) {
		n.warnUndeclared(MeasureResources)
		resource, settled = nil, false
	}
	n.emitLocked(status, resource, settled, inResult, "")
}

// mergeLocked takes the fields sample sets that the node declares, warning of the rest.
func (n *Node) mergeLocked(sample Sample) {
	if sample.Bytes != nil {
		if n.declares(MeasureBytes) {
			n.sample.Bytes = newCopy(sample.Bytes)
		} else {
			n.warnUndeclared(MeasureBytes)
		}
	}
	if sample.Items != nil {
		if n.declares(MeasureItems) && !n.batch {
			n.sample.Items = newCopy(sample.Items)
		} else {
			n.warnUndeclared(MeasureItems)
		}
	}
}

func (n *Node) warnUndeclared(measure Measure) {
	n.logger.Warn().
		Str("kind", string(n.step.Kind)).
		Str("measure", string(measure)).
		Msg("progress: the node does not report this measure, dropped")
}

// countChildLocked counts a child's outcome on a batch and reports the new count.
func (n *Node) countChildLocked(status Status) {
	if n == nil || !n.batch || n.ended {
		return
	}
	items := *n.sample.Items
	switch status {
	case StatusSucceeded:
		items.Done++
	case StatusFailed:
		items.Failed++
	case StatusCancelled:
		items.Cancelled++
	}
	n.sample.Items = &items
	n.emitLocked(StatusInProgress, nil, false, false, "")
}

// emitLocked reports one event, opening the node, and its unopened ancestors, first.
func (n *Node) emitLocked(status Status, resource *Resource, settled, inResult bool, errText string) {
	n.openLocked()
	n.tracker.reporter.Report(n.newEventLocked(status, nil, resource, settled, inResult, errText))
}

func (n *Node) openLocked() {
	if n.opened {
		return
	}
	var parentID NodeID
	if n.parent != nil {
		n.parent.openLocked()
		parentID = n.parent.id
	}
	n.tracker.lastID++
	n.id = n.tracker.lastID
	n.opened = true
	open := Open{
		Parent:   parentID,
		Step:     n.step,
		Measures: n.measures,
	}
	n.tracker.reporter.Report(n.newEventLocked(StatusStarted, &open, nil, false, false, ""))
}

// newEventLocked snapshots the node; the sample is copied, so a reporter may keep the event.
func (n *Node) newEventLocked(
	status Status,
	open *Open,
	resource *Resource,
	settled bool,
	inResult bool,
	errText string,
) Event {
	now := n.tracker.now()
	return Event{
		Node:     n.id,
		Status:   status,
		Open:     open,
		Msg:      n.msg,
		Error:    errText,
		Resource: resource,
		Settled:  settled,
		InResult: inResult,
		Sample:   newSampleCopy(n.sample, now.Sub(n.startedAt)),
		At:       now,
	}
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
