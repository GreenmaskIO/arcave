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

package tty

import (
	"slices"
	"time"

	"github.com/greenmaskio/arcave/pkg/progress"
)

// tree is the stream read so far: one entry per node, the roots in the order they opened.
type tree struct {
	nodes map[progress.StepID]*node
	roots []progress.StepID
}

type node struct {
	open progress.Open
	last progress.Event
	// msg is the node's own message, the last Update's.
	msg       string
	children  []progress.StepID
	resources []resourceLine
	warnings  []warning
	startedAt time.Time
	endedAt   time.Time
}

// warning is one warning of a node's own, not about a resource.
type warning struct {
	msg string
	// inResult is a warning the operation's result repeats.
	inResult bool
}

// resourceLine is the last message about one resource of a summary activity.
type resourceLine struct {
	key      string
	resource progress.Resource
	msg      string
	warned   bool
}

func newTree() *tree {
	return &tree{nodes: make(map[progress.StepID]*node)}
}

// apply reads evt into the tree. An event of a node whose started was never read is dropped.
func (t *tree) apply(evt progress.Event) {
	if evt.Status == progress.StatusStarted && evt.Open != nil {
		t.openNode(evt)
		return
	}
	current, ok := t.nodes[evt.StepID]
	if !ok {
		return
	}
	switch {
	case evt.Resource != nil:
		current.resources = withResource(current.resources, evt)
	case evt.Status == progress.StatusWarned:
		current.warnings = append(current.warnings, warning{
			msg:      evt.Msg,
			inResult: evt.InResult,
		})
	default:
		current.msg = evt.Msg
	}
	current.last = evt
	if evt.Status.IsTerminal() {
		current.endedAt = evt.At
	}
}

func (t *tree) openNode(evt progress.Event) {
	if _, ok := t.nodes[evt.StepID]; ok {
		return
	}
	parent, ok := t.nodes[evt.Open.Parent]
	switch {
	case evt.Open.Parent == 0:
		t.roots = append(t.roots, evt.StepID)
	case ok:
		parent.children = append(parent.children, evt.StepID)
	default:
		return
	}
	t.nodes[evt.StepID] = &node{
		open:      *evt.Open,
		last:      evt,
		msg:       evt.Msg,
		startedAt: evt.At.Add(-evt.Sample.Elapsed),
	}
}

// withResource sets the line of evt's resource, keyed kind/id, a new one going last.
func withResource(lines []resourceLine, evt progress.Event) []resourceLine {
	line := resourceLine{
		key:      string(evt.Resource.Kind) + "/" + evt.Resource.ID,
		resource: *evt.Resource,
		msg:      evt.Msg,
		warned:   evt.Status == progress.StatusWarned,
	}
	i := slices.IndexFunc(lines, func(l resourceLine) bool { return l.key == line.key })
	if i < 0 {
		return append(lines, line)
	}
	lines[i] = line
	return lines
}

// snapshot copies the tree for a render outside the lock. Events are shared: no one mutates one.
func (t *tree) snapshot() *tree {
	nodes := make(map[progress.StepID]*node, len(t.nodes))
	for id, current := range t.nodes {
		copied := *current
		copied.children = slices.Clone(current.children)
		copied.resources = slices.Clone(current.resources)
		copied.warnings = slices.Clone(current.warnings)
		nodes[id] = &copied
	}
	return &tree{
		nodes: nodes,
		roots: slices.Clone(t.roots),
	}
}
