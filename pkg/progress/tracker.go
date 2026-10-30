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

// Reporter renders a progress stream. A Tracker calls it under its own lock, one event at a time.
type Reporter interface {
	Report(evt Event)
}

type (
	nodeKey    struct{}
	trackerKey struct{}
)

// Tracker numbers the nodes of one operation and hands their events to a Reporter.
type Tracker struct {
	reporter Reporter
	now      func() time.Time
	interval time.Duration

	mu     sync.Mutex
	lastID NodeID
}

// New returns a Tracker reporting to reporter, timing on now.
func New(reporter Reporter, now func() time.Time) *Tracker {
	return &Tracker{
		reporter: reporter,
		now:      now,
		interval: defaultSampleInterval,
	}
}

// Discard returns a Tracker whose every node is mute.
func Discard() *Tracker {
	return nil
}

// Begin opens the root workflow, lazily, whatever node ctx carries.
func (t *Tracker) Begin(ctx context.Context, step Step) (context.Context, *Node) {
	if t == nil {
		return withNode(ctx, muteNode), muteNode
	}
	node := t.newNode(ctx, nil, step, NodeTypeWorkflow, nil, Sample{})
	return withNode(ctx, node), node
}

// WithTracker returns ctx carrying t: a Begin on it with no node opens t's root.
func WithTracker(ctx context.Context, t *Tracker) context.Context {
	return context.WithValue(ctx, trackerKey{}, t)
}

// Begin opens a workflow under the node of ctx, or the root of the tracker ctx carries when it
// carries no node. It reports nothing until its first child or Warn.
func Begin(ctx context.Context, step Step) (context.Context, *Node) {
	if _, ok := ctx.Value(nodeKey{}).(*Node); !ok {
		if tracker, ok := ctx.Value(trackerKey{}).(*Tracker); ok {
			return tracker.Begin(ctx, step)
		}
	}
	parent, ok := findParent(ctx, step)
	if !ok {
		return ctx, muteNode
	}
	node := parent.tracker.newNode(ctx, parent, step, NodeTypeWorkflow, nil, Sample{})
	return withNode(ctx, node), node
}

// Batch opens a loop over total objects under the node of ctx, at once; it counts its
// children's outcomes itself. A total of 0 is unknown.
func Batch(ctx context.Context, step Step, total int) (context.Context, *Node) {
	parent, ok := findParent(ctx, step)
	if !ok {
		return ctx, muteNode
	}
	var (
		measures = []Measure{MeasureItems}
		sample   = Sample{Items: &Items{Total: int64(total)}}
		node     = parent.tracker.newNode(ctx, parent, step, NodeTypeWorkflow, measures, sample)
	)
	node.tracker.mu.Lock()
	defer node.tracker.mu.Unlock()
	node.openLocked()
	return withNode(ctx, node), node
}

// Run opens an activity under the node of ctx, at once, reporting the measures it declares.
func Run(ctx context.Context, step Step, measures ...Measure) (context.Context, *Node) {
	parent, ok := findParent(ctx, step)
	if !ok {
		return ctx, muteNode
	}
	node := parent.tracker.newNode(ctx, parent, step, NodeTypeActivity, measures, Sample{})
	node.tracker.mu.Lock()
	defer node.tracker.mu.Unlock()
	node.openLocked()
	return withNode(ctx, node), node
}

// WithNode returns ctx carrying node: the work that follows an activity, on a context derived
// from the activity's, goes on under node again.
func WithNode(ctx context.Context, node *Node) context.Context {
	return withNode(ctx, node)
}

// Current returns the node of ctx, a mute one when ctx carries none.
func Current(ctx context.Context) *Node {
	if node, ok := ctx.Value(nodeKey{}).(*Node); ok {
		return node
	}
	return muteNode
}

func (t *Tracker) newNode(
	ctx context.Context,
	parent *Node,
	step Step,
	nodeType NodeType,
	measures []Measure,
	sample Sample,
) *Node {
	step.Type = nodeType
	return &Node{
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

func withNode(ctx context.Context, node *Node) context.Context {
	return context.WithValue(ctx, nodeKey{}, node)
}

// findParent returns the node of ctx a child may open under: neither mute, an activity nor ended.
func findParent(ctx context.Context, step Step) (*Node, bool) {
	parent := Current(ctx)
	if parent.isMute() {
		return nil, false
	}
	if parent.step.Type == NodeTypeActivity {
		zerolog.Ctx(ctx).Warn().
			Str("kind", string(step.Kind)).
			Str("parent", string(parent.step.Kind)).
			Msg("progress: an activity has no children, the node is mute")
		return nil, false
	}
	if parent.isEnded() {
		zerolog.Ctx(ctx).Warn().
			Str("kind", string(step.Kind)).
			Str("parent", string(parent.step.Kind)).
			Msg("progress: the parent has ended, the node is mute")
		return nil, false
	}
	return parent, true
}
