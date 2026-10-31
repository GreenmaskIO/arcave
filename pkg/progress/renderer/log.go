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

package renderer

import (
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/pkg/progress"
	"github.com/greenmaskio/arcave/pkg/progress/renderer/internal/format"
)

// Log writes the stream as log records, one per event worth a line, each named by its node's
// path: album.publish[summer]/album.export/photo.resize/photo[beach.jpg].
type Log struct {
	logger zerolog.Logger
	nodes  map[progress.StepID]logNode
}

type logNode struct {
	path  string
	msg   string
	items progress.Items
}

// NewLog returns a Log renderer writing to logger.
func NewLog(logger zerolog.Logger) *Log {
	return &Log{
		logger: logger,
		nodes:  make(map[progress.StepID]logNode),
	}
}

// Render logs evt unless it is a tick changing neither its message nor its count: bytes alone
// are not worth a line. A node is forgotten once it ends.
func (r *Log) Render(evt progress.Event) {
	node := r.nodes[evt.StepID]
	if evt.Open != nil {
		node = logNode{path: formatPath(r.nodes[evt.Open.Parent].path, evt.Open.Step)}
	}
	items := toItems(evt.Sample.Items)
	skip := evt.Status == progress.StatusInProgress &&
		evt.Msg == node.msg && evt.Resource == nil && items == node.items
	// A warning's or a resource's message is its event's alone: the node keeps its own.
	msg := evt.Msg
	if evt.Resource != nil || evt.Status == progress.StatusWarned {
		msg = node.msg
	}
	if evt.Status.IsTerminal() {
		delete(r.nodes, evt.StepID)
	} else {
		r.nodes[evt.StepID] = logNode{
			path:  node.path,
			msg:   msg,
			items: items,
		}
	}
	if skip {
		return
	}
	r.newRecord(evt).Msg(node.path)
}

// toItems returns the count items points to, a zero one for nil.
func toItems(items *progress.Items) progress.Items {
	if items == nil {
		return progress.Items{}
	}
	return *items
}

func (r *Log) newRecord(evt progress.Event) *zerolog.Event {
	record := r.logger.WithLevel(toLevel(evt)).Str("status", string(evt.Status))
	if evt.Msg != "" {
		record = record.Str("msg", evt.Msg)
	}
	if evt.Error != "" {
		record = record.Str("error", evt.Error)
	}
	if evt.Resource != nil {
		record = record.Str("resource", string(evt.Resource.Kind)+":"+evt.Resource.ID)
	}
	if bytes := evt.Sample.Bytes; bytes != nil {
		record = record.Str("bytes", formatBytes(*bytes))
		if bytes.Stored != 0 {
			record = record.Str("stored", format.Size(bytes.Stored))
		}
		if bytes.Reused != 0 {
			record = record.Str("reused", format.Size(bytes.Reused))
		}
	}
	if items := evt.Sample.Items; items != nil {
		record = record.Str("items", formatItems(*items))
		if items.Failed != 0 {
			record = record.Int64("failed", items.Failed)
		}
		if items.Cancelled != 0 {
			record = record.Int64("cancelled", items.Cancelled)
		}
	}
	if evt.Status.IsTerminal() {
		record = record.Str("elapsed", evt.Sample.Elapsed.Round(time.Millisecond).String())
	}
	return record
}

// toLevel is the level evt is logged at: a resource still under way is debug, so --log-level info
// shows only what each resource settled into.
func toLevel(evt progress.Event) zerolog.Level {
	switch {
	case evt.Status == progress.StatusFailed:
		return zerolog.ErrorLevel
	case evt.Status == progress.StatusWarned, evt.Status == progress.StatusCancelled:
		return zerolog.WarnLevel
	case evt.Status == progress.StatusInProgress && evt.Resource != nil && !evt.Settled:
		return zerolog.DebugLevel
	default:
		return zerolog.InfoLevel
	}
}

// formatPath appends step to the parent's path: its kind, then its name or id in brackets.
func formatPath(parent string, step progress.Step) string {
	segment := string(step.Kind)
	switch {
	case step.Name != "":
		segment += "[" + step.Name + "]"
	case step.ID != "":
		segment += "[" + step.ID + "]"
	}
	if parent == "" {
		return segment
	}
	return parent + "/" + segment
}

// formatBytes renders done/total, or done alone while the total is unknown or exceeded.
func formatBytes(bytes progress.Bytes) string {
	if bytes.Total == 0 || bytes.Done > bytes.Total {
		return format.Size(bytes.Done)
	}
	return format.Size(bytes.Done) + "/" + format.Size(bytes.Total)
}

// formatItems renders done/total, or done alone while the total is unknown.
func formatItems(items progress.Items) string {
	if items.Total == 0 {
		return fmt.Sprint(items.Done)
	}
	return fmt.Sprintf("%d/%d", items.Done, items.Total)
}
