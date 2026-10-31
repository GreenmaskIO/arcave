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
	"io"
	"sync"
	"time"
)

// Sampler accumulates a step's sample for a source deep in the call and sends it at most once
// per the tracker's interval. The last value is not flushed: the step's Done carries the total.
type Sampler struct {
	topic    *StepReporter
	template Sample

	mu     sync.Mutex
	sample Sample
	sent   bool
	sentAt time.Time
}

// NewSampler returns a Sampler for topic, its sample shaped by template and starting from its
// values. It is mute when topic is, or when topic does not declare a measure template sets.
func NewSampler(topic *StepReporter, template Sample) *Sampler {
	if topic.isMute() || !topic.declaresSample(template) {
		return &Sampler{}
	}
	return &Sampler{
		topic:    topic,
		template: newSampleCopy(template, 0),
		sample:   newSampleCopy(template, 0),
	}
}

// Update changes the accumulated sample through fn — adding to it or replacing it — and sends
// it when the interval has passed. Fields template does not set are dropped.
func (s *Sampler) Update(fn func(sample *Sample)) {
	if s.topic == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.sample)
	s.sample = newShapedSample(s.sample, s.template)
	now := s.topic.tracker.now()
	if s.sent && now.Sub(s.sentAt) < s.topic.tracker.interval {
		return
	}
	s.sent = true
	s.sentAt = now
	s.topic.Progress(s.sample)
}

func (s *Sampler) addBytes(n int) {
	if n == 0 {
		return
	}
	s.Update(func(sample *Sample) {
		sample.Bytes.Done += int64(n)
	})
}

type reader struct {
	r       io.Reader
	sampler *Sampler
}

// NewReader counts the bytes read from r on the step of ctx, out of total, 0 if unknown. It
// returns r itself when that step does not report bytes.
func NewReader(ctx context.Context, r io.Reader, total int64) io.Reader {
	sampler := newBytesSampler(ctx, total)
	if sampler.topic == nil {
		return r
	}
	return &reader{
		r:       r,
		sampler: sampler,
	}
}

func (r *reader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.sampler.addBytes(n)
	return n, err
}

type writer struct {
	w       io.Writer
	sampler *Sampler
}

// NewWriter counts the bytes written to w on the step of ctx, out of total, 0 if unknown. It
// returns w itself when that step does not report bytes.
func NewWriter(ctx context.Context, w io.Writer, total int64) io.Writer {
	sampler := newBytesSampler(ctx, total)
	if sampler.topic == nil {
		return w
	}
	return &writer{
		w:       w,
		sampler: sampler,
	}
}

func (w *writer) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.sampler.addBytes(n)
	return n, err
}

func newBytesSampler(ctx context.Context, total int64) *Sampler {
	return NewSampler(Current(ctx), Sample{Bytes: &Bytes{Total: total}})
}

// newShapedSample keeps the fields of sample that template sets, a zero one for each fn dropped.
func newShapedSample(sample, template Sample) Sample {
	return Sample{
		Bytes: newShapedField(sample.Bytes, template.Bytes != nil),
		Items: newShapedField(sample.Items, template.Items != nil),
	}
}

func newShapedField[T any](v *T, keep bool) *T {
	switch {
	case !keep:
		return nil
	case v == nil:
		return new(T)
	default:
		return v
	}
}
