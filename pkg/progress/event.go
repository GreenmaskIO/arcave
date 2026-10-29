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

import "time"

// A progress stream is a sequence of Events over a tree of nodes. A tree such as
//
//	album.publish "summer"          workflow
//	├─ queue.pause                  activity  [resources]
//	└─ cdn.upload                   workflow  [items]      a batch
//	   ├─ file "beach.jpg"          activity  [bytes]
//	   └─ file "dunes.jpg"          activity  [bytes]
//
// comes as (at and elapsed_ns left out):
//
//	{"node":1,"status":"started","open":{"step":{"kind":"album.publish","type":"workflow","name":"summer"}}}
//	{"node":2,"status":"started","open":{"parent":1,"step":{"kind":"queue.pause","type":"activity"},"measures":["resources"]}}
//	{"node":2,"status":"in_progress","msg":"Paused","resource":{"kind":"worker","id":"worker-1"}}
//	{"node":2,"status":"succeeded","msg":"Paused"}
//	{"node":3,"status":"started","open":{"parent":1,"step":{"kind":"cdn.upload","type":"workflow"},"measures":["items"]},"sample":{"items":{"done":0,"total":2}}}
//	{"node":4,"status":"started","open":{"parent":3,"step":{"kind":"file","type":"activity","id":"beach.jpg"},"measures":["bytes"]}}
//	{"node":4,"status":"in_progress","sample":{"bytes":{"done":1050000,"total":2100000}}}
//	{"node":4,"status":"succeeded","sample":{"bytes":{"done":2100000,"total":2100000}}}
//	{"node":3,"status":"in_progress","sample":{"items":{"done":1,"total":2}}}
//	{"node":5,"status":"started","open":{"parent":3,"step":{"kind":"file","type":"activity","id":"dunes.jpg"},"measures":["bytes"]}}
//	{"node":5,"status":"failed","msg":"unable to upload","error":"403 forbidden","sample":{"bytes":{"done":450000}}}
//	{"node":3,"status":"in_progress","sample":{"items":{"done":1,"failed":1,"total":2}}}
//	{"node":3,"status":"failed","sample":{"items":{"done":1,"failed":1,"total":2}}}
//	{"node":1,"status":"failed"}
//
// A reader reads the stream from its start and keeps one entry per node:
//
//   - started: the node is new; Open names its parent (0 for the root, which always opens
//     first), its step and its measures — the widget to build, once.
//   - any later event: replace the node's state with it whole — msg, sample, resource — since
//     every event is a full snapshot, not a delta.
//   - an in_progress event with a resource: that resource's line, under way, or settled into
//     its outcome when Settled is set.
//   - succeeded, failed, cancelled: the node's last event. Error is set only on the node the
//     failure arose on; its failed ancestors carry none.

// NodeType says whether a node is made of other nodes or is one action.
type NodeType string

const (
	// NodeTypeWorkflow is a node made of other nodes, a batch included.
	NodeTypeWorkflow NodeType = "workflow"
	// NodeTypeActivity is one action; it has no children.
	NodeTypeActivity NodeType = "activity"
)

// Measure names what a node reports, and so which widget renders it. A node declaring
// none is a spinner: a status and a message.
type Measure string

const (
	// MeasureBytes is a byte bar, Sample.Bytes: a copy or a transfer of one object
	// (2.1MB/4.0MB). Without a Total it is a running count.
	MeasureBytes Measure = "bytes"
	// MeasureItems is a counter, Sample.Items: a batch's finished children (3/5 failed=1).
	MeasureItems Measure = "items"
	// MeasureResources is a line per resource, Event.Resource: the set one call
	// handles, as compose's per-container rows (worker-1 Paused).
	MeasureResources Measure = "resources"
)

// Status is the state an event reports a node in.
type Status string

const (
	StatusStarted    Status = "started"
	StatusInProgress Status = "in_progress"
	StatusWarned     Status = "warned"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
)

// IsTerminal reports whether no event follows this one on its node.
func (s Status) IsTerminal() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCancelled
}

// Kind names a node: "<namespace>.<action>" for an action, a bare noun for an object.
// The consumer declares its kinds.
type Kind string

// ResourceKind names what a resource of a summary activity is. The consumer declares it.
type ResourceKind string

// NodeID numbers a node within one operation, from 1.
type NodeID uint64

// Step says what a node does.
type Step struct {
	Kind Kind     `json:"kind"`
	Type NodeType `json:"type"`
	// ID is the object's resource key under a batch; empty elsewhere.
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// Open describes a node once, on its started event: where it is and how to render it.
type Open struct {
	// Parent is 0 for the root.
	Parent   NodeID    `json:"parent,omitempty"`
	Step     Step      `json:"step"`
	Measures []Measure `json:"measures,omitempty"`
}

// Resource is one resource a summary activity handles.
type Resource struct {
	Kind ResourceKind `json:"kind"`
	ID   string       `json:"id"`
	Name string       `json:"name,omitempty"`
}

// Bytes counts bytes, accumulated; a Total of 0 is unknown.
type Bytes struct {
	Done   int64 `json:"done"`
	Total  int64 `json:"total,omitempty"`
	Stored int64 `json:"stored,omitempty"`
	Reused int64 `json:"reused,omitempty"`
}

// Items counts items, accumulated: Done only the succeeded; a Total of 0 is unknown.
type Items struct {
	Done      int64 `json:"done"`
	Failed    int64 `json:"failed,omitempty"`
	Cancelled int64 `json:"cancelled,omitempty"`
	Total     int64 `json:"total,omitempty"`
}

// Sample is a node's accumulated state at an event.
type Sample struct {
	Elapsed time.Duration `json:"elapsed_ns"`
	Bytes   *Bytes        `json:"bytes,omitempty"`
	Items   *Items        `json:"items,omitempty"`
}

// Event is one event of a progress stream, a full snapshot of its node.
type Event struct {
	Node   NodeID `json:"node"`
	Status Status `json:"status"`
	// Open is set on started only.
	Open *Open  `json:"open,omitempty"`
	Msg  string `json:"msg,omitempty"`
	// Error is set on failed only, on the node the error arose on.
	Error    string    `json:"error,omitempty"`
	Resource *Resource `json:"resource,omitempty"`
	// Settled marks an in_progress event of a resource that has reached its outcome, while the
	// node goes on; one without it is the resource still under way.
	Settled bool `json:"settled,omitempty"`
	// InResult marks a warning the operation's result repeats: a renderer beside the result may
	// leave it out.
	InResult bool      `json:"in_result,omitempty"`
	Sample   Sample    `json:"sample"`
	At       time.Time `json:"at"`
}
