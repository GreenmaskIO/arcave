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

package progress_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/pkg/progress"
)

func TestSampler_Update(t *testing.T) {
	tests := []struct {
		name     string
		template progress.Sample
		run      func(sampler *progress.Sampler, clock *exampleClock)
		want     []string
	}{
		{
			name:     "starts from the template and adds",
			template: progress.Sample{Bytes: &progress.Bytes{Total: 10}},
			run: func(sampler *progress.Sampler, _ *exampleClock) {
				sampler.Update(func(sample *progress.Sample) { sample.Bytes.Done += 3 })
			},
			want: []string{"2 in_progress bytes=3/10"},
		},
		{
			name:     "replaces with numbers the source counted itself",
			template: progress.Sample{Bytes: &progress.Bytes{}},
			run: func(sampler *progress.Sampler, clock *exampleClock) {
				sampler.Update(func(sample *progress.Sample) { sample.Bytes.Done = 5 })
				clock.advance(time.Second)
				sampler.Update(func(sample *progress.Sample) {
					sample.Bytes.Done = 7
					sample.Bytes.Total = 9
				})
			},
			want: []string{"2 in_progress bytes=5/0", "2 in_progress bytes=7/9"},
		},
		{
			name:     "drops the fields the template does not set",
			template: progress.Sample{Bytes: &progress.Bytes{}},
			run: func(sampler *progress.Sampler, _ *exampleClock) {
				sampler.Update(func(sample *progress.Sample) {
					sample.Bytes.Done = 1
					sample.Items = &progress.Items{Done: 1}
				})
			},
			want: []string{"2 in_progress bytes=1/0"},
		},
		{
			name:     "restores a field of the template the source dropped",
			template: progress.Sample{Bytes: &progress.Bytes{}},
			run: func(sampler *progress.Sampler, clock *exampleClock) {
				sampler.Update(func(sample *progress.Sample) { sample.Bytes = nil })
				clock.advance(time.Second)
				sampler.Update(func(sample *progress.Sample) { sample.Bytes.Done++ })
			},
			want: []string{"2 in_progress bytes=0/0", "2 in_progress bytes=1/0"},
		},
		{
			name:     "sends the first update at once, then once per interval",
			template: progress.Sample{Bytes: &progress.Bytes{}},
			run: func(sampler *progress.Sampler, clock *exampleClock) {
				for range 5 {
					sampler.Update(func(sample *progress.Sample) { sample.Bytes.Done++ })
					clock.advance(100 * time.Millisecond)
				}
			},
			want: []string{"2 in_progress bytes=1/0", "2 in_progress bytes=3/0", "2 in_progress bytes=5/0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker, events, clock := newTestTracker()
			_, node := progress.Run(withRoot(context.Background(), tracker), newStepPhoto("beach.jpg"),
				progress.MeasureBytes, progress.MeasureItems)
			sampler := progress.NewSampler(node, tt.template)

			tt.run(sampler, clock)

			require.Greater(t, len(events.events), 2)
			assert.Equal(t, tt.want, events.lines()[2:])
		})
	}
}

func TestSampler_Update_Concurrent(t *testing.T) {
	const writers = 20
	tracker, events, clock := newTestTracker()
	_, node := progress.Run(withRoot(context.Background(), tracker), newStepPhoto("beach.jpg"), progress.MeasureBytes)
	sampler := progress.NewSampler(node, progress.Sample{Bytes: &progress.Bytes{}})
	sampler.Update(func(*progress.Sample) {})

	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			for range 100 {
				sampler.Update(func(sample *progress.Sample) { sample.Bytes.Done++ })
			}
		})
	}
	wg.Wait()
	clock.advance(time.Second)
	sampler.Update(func(*progress.Sample) {})

	require.NotEmpty(t, events.events)
	assert.Equal(t, "2 in_progress bytes=2000/0", formatEvent(events.events[len(events.events)-1]))
}

func TestSampler_Update_AfterTheNodeEnded(t *testing.T) {
	tracker, events, _ := newTestTracker()
	_, node := progress.Run(withRoot(context.Background(), tracker), newStepPhoto("beach.jpg"), progress.MeasureBytes)
	sampler := progress.NewSampler(node, progress.Sample{Bytes: &progress.Bytes{}})
	node.End(nil)

	sampler.Update(func(sample *progress.Sample) { sample.Bytes.Done = 1 })

	assert.Equal(t, []string{
		"1 started <0 album.publish workflow",
		"2 started <1 photo activity [bytes]",
		"2 succeeded",
	}, events.lines())
}

func TestNewReader(t *testing.T) {
	tracker, events, clock := newTestTracker()
	ctx, _ := progress.Run(withRoot(context.Background(), tracker), newStepPhoto("beach.jpg"), progress.MeasureBytes)
	src := progress.NewReader(ctx, iotest.OneByteReader(strings.NewReader("abc")), 0)

	clock.advance(time.Second)
	buf := make([]byte, 8)
	n, err := src.Read(buf)
	clock.advance(time.Second)
	rest, restErr := io.ReadAll(src)

	require.NoError(t, err)
	require.NoError(t, restErr)
	require.Len(t, events.events, 4)
	assert.Equal(t, 1, n)
	assert.Equal(t, "bc", string(rest))
	assert.Equal(t, "2 in_progress bytes=1/0", formatEvent(events.events[2]))
	assert.Equal(t, "2 in_progress bytes=2/0", formatEvent(events.events[3]), "one send per interval")
}

func TestNewReader_UnknownTotalIsLeftOut(t *testing.T) {
	tracker, events, _ := newTestTracker()
	ctx, _ := progress.Run(withRoot(context.Background(), tracker), newStepPhoto("beach.jpg"), progress.MeasureBytes)
	_, err := io.ReadAll(progress.NewReader(ctx, strings.NewReader("ab"), 0))
	require.NoError(t, err)
	require.Len(t, events.events, 3)

	raw, err := json.Marshal(events.events[2].Sample)

	require.NoError(t, err)
	assert.JSONEq(t, `{"elapsed_ns":0,"bytes":{"done":2}}`, string(raw))
}

func TestNewReader_PassesTheSourceErrorThrough(t *testing.T) {
	errDisk := errors.New("disk gone")
	tracker, events, _ := newTestTracker()
	ctx, _ := progress.Run(withRoot(context.Background(), tracker), newStepPhoto("beach.jpg"), progress.MeasureBytes)
	src := progress.NewReader(ctx, io.MultiReader(strings.NewReader("ab"), iotest.ErrReader(errDisk)), 10)

	data, err := io.ReadAll(src)

	require.ErrorIs(t, err, errDisk)
	assert.Equal(t, "ab", string(data))
	require.Len(t, events.events, 3)
	assert.Equal(t, "2 in_progress bytes=2/10", formatEvent(events.events[2]))
}

func TestNewWriter(t *testing.T) {
	tracker, events, _ := newTestTracker()
	ctx, _ := progress.Run(withRoot(context.Background(), tracker), newStepPhoto("beach.jpg"), progress.MeasureBytes)
	var dst bytes.Buffer

	n, err := progress.NewWriter(ctx, &dst, 4).Write([]byte("abc"))

	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, "abc", dst.String())
	require.Len(t, events.events, 3)
	assert.Equal(t, "2 in_progress bytes=3/4", formatEvent(events.events[2]))
}

func TestNewWriter_PassesTheSinkErrorThrough(t *testing.T) {
	errDisk := errors.New("disk gone")
	tracker, events, _ := newTestTracker()
	ctx, _ := progress.Run(withRoot(context.Background(), tracker), newStepPhoto("beach.jpg"), progress.MeasureBytes)

	n, err := progress.NewWriter(ctx, &shortWriter{n: 2, err: errDisk}, 10).Write([]byte("abc"))

	require.ErrorIs(t, err, errDisk)
	require.Len(t, events.events, 3)
	assert.Equal(t, 2, n)
	assert.Equal(t, "2 in_progress bytes=2/10", formatEvent(events.events[2]))
}

// shortWriter accepts n bytes of any write and fails it with err.
type shortWriter struct {
	n   int
	err error
}

func (w *shortWriter) Write([]byte) (int, error) {
	return w.n, w.err
}

func TestNewSampler(t *testing.T) {
	tests := []struct {
		name string
		ctx  func(tracker *progress.Tracker) context.Context
	}{
		{
			name: "no node in the context",
			ctx: func(*progress.Tracker) context.Context {
				return context.Background()
			},
		},
		{
			name: "a discarding tracker",
			ctx: func(*progress.Tracker) context.Context {
				ctx, _ := progress.Discard().Begin(context.Background(), stepPublish)
				return ctx
			},
		},
		{
			name: "a node not reporting bytes",
			ctx: func(tracker *progress.Tracker) context.Context {
				ctx, _ := progress.Run(withRoot(context.Background(), tracker), stepWrite)
				return ctx
			},
		},
		{
			name: "a batch, whose bytes are its children's",
			ctx: func(tracker *progress.Tracker) context.Context {
				ctx, _ := progress.Batch(withRoot(context.Background(), tracker), stepResize, 1)
				return ctx
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker, events, _ := newTestTracker()
			ctx := tt.ctx(tracker)
			var (
				src = strings.NewReader("abc")
				dst bytes.Buffer
			)
			opened := len(events.events)

			reader := progress.NewReader(ctx, src, 3)
			writer := progress.NewWriter(ctx, &dst, 3)
			progress.NewSampler(progress.Current(ctx), progress.Sample{Bytes: &progress.Bytes{}}).
				Update(func(sample *progress.Sample) { sample.Bytes.Done = 1 })

			assert.Same(t, src, reader)
			assert.Same(t, &dst, writer)
			assert.Len(t, events.events, opened)
		})
	}
}
