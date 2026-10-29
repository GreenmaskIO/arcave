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
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/pkg/progress"
)

var start = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.now = c.now.Add(d)
}

type recorder struct {
	events []progress.Event
}

func (r *recorder) Report(evt progress.Event) {
	r.events = append(r.events, evt)
}

// newStream runs workflow on a real Tracker and returns the stream it reported.
func newStream(t *testing.T, workflow func(ctx context.Context, tracker *progress.Tracker, clock *fakeClock)) []progress.Event {
	t.Helper()
	var (
		clock = &fakeClock{now: start}
		rec   = &recorder{}
		ctx   = zerolog.Nop().WithContext(context.Background())
	)
	workflow(ctx, progress.New(rec, clock.Now), clock)
	return rec.events
}

func newTreeOf(events []progress.Event) *tree {
	t := newTree()
	for _, evt := range events {
		t.apply(evt)
	}
	return t
}

func ms(n int) time.Duration {
	return time.Duration(n) * time.Millisecond
}
