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

// Package heartbeat keeps a running operation's heartbeat fresh, and reads one back.
package heartbeat

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

const (
	// DefaultInterval is how often a running operation rewrites its heartbeat.
	DefaultInterval = 5 * time.Minute
	// DefaultStaleAfter is how long an in-progress heartbeat stays alive unrewritten, measured
	// from the store's LastModified, never from UpdatedAt, which is the writer's clock.
	DefaultStaleAfter = 30 * time.Minute
	// WriteTimeout bounds one write, the closing one included.
	WriteTimeout = 5 * time.Second
)

type heartbeatWriter interface {
	Write(ctx context.Context, beat Heartbeat) error
}

// Worker reports a running operation in its heartbeat. A failed beat is retried on the next
// tick; once the last good one is Deadline old, the operation is cancelled.
type Worker struct {
	writer     heartbeatWriter
	base       Heartbeat
	interval   time.Duration
	staleAfter time.Duration
	quit       chan struct{}
	done       chan struct{}

	mu   sync.Mutex
	last time.Time
}

// NewWorker returns a worker writing through w every DefaultInterval.
func NewWorker(writer heartbeatWriter) *Worker {
	return &Worker{
		writer:     writer,
		interval:   DefaultInterval,
		staleAfter: DefaultStaleAfter,
		quit:       make(chan struct{}),
		done:       make(chan struct{}),
	}
}

// SetInterval sets how often the worker beats.
func (s *Worker) SetInterval(interval time.Duration) *Worker {
	s.interval = interval
	return s
}

// SetStaleAfter sets the age at which a gc may take the heartbeat's tree for dead.
func (s *Worker) SetStaleAfter(staleAfter time.Duration) *Worker {
	s.staleAfter = staleAfter
	return s
}

// Deadline is the age at which a last good beat stops the operation: one interval short of
// the stale age, so the tick that reaches it comes before any gc sees the tree stale.
func (s *Worker) Deadline() time.Duration {
	return s.staleAfter - s.interval
}

// Start writes the first beat of base's operation, then beats on every tick until Stop; a
// heartbeat that cannot be kept alive cancels with model.ErrHeartbeatLost.
func (s *Worker) Start(ctx context.Context, base Heartbeat, cancel context.CancelCauseFunc) error {
	s.base = base
	s.base.StartedAt = wallClock()
	if err := s.beat(ctx, StatusInProgress, nil); err != nil {
		return fmt.Errorf("%w: %w", model.ErrHeartbeatLost, err)
	}
	go s.run(ctx, cancel)
	return nil
}

// Stop ends the beats and waits for the last one to finish.
func (s *Worker) Stop() {
	close(s.quit)
	<-s.done
}

// Alive refuses an operation whose last good beat is Deadline old or older; call it after Stop.
func (s *Worker) Alive() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if age := wallClock().Sub(s.last); age >= s.Deadline() {
		return fmt.Errorf("%w: last written %s ago", model.ErrHeartbeatLost, age.Round(time.Second))
	}
	return nil
}

// Finish marks the operation done; a failed mark is only logged, the record outranks it.
func (s *Worker) Finish(ctx context.Context) {
	if err := s.beat(ctx, StatusDone, nil); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("marking the operation done")
	}
}

// Fail marks the operation failed with cause, even past a cancelled command; a failed mark is
// only logged.
func (s *Worker) Fail(ctx context.Context, cause error) {
	if err := s.beat(context.WithoutCancel(ctx), StatusFailed, cause); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("marking the operation failed")
	}
}

// run beats on every tick until Stop or ctx ends. A tick past the deadline cancels without
// writing: a fresh beat would revive a tree a gc may already be taking.
func (s *Worker) run(ctx context.Context, cancel context.CancelCauseFunc) {
	defer close(s.done)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.quit:
			return
		case <-ticker.C:
			if err := s.Alive(); err != nil {
				zerolog.Ctx(ctx).Error().Err(err).Msg("cancelling the operation")
				cancel(err)
				return
			}
			if err := s.beat(ctx, StatusInProgress, nil); err != nil {
				zerolog.Ctx(ctx).Warn().Err(err).Msg("heartbeat failed; retrying on the next tick")
			}
		}
	}
}

// beat writes one heartbeat; a good one moves the deadline.
func (s *Worker) beat(ctx context.Context, status Status, cause error) error {
	at := wallClock()
	beat := s.base
	beat.Status = status
	beat.UpdatedAt = at
	if status != StatusInProgress {
		beat.FinishedAt = at
	}
	if cause != nil {
		beat.Error = cause.Error()
	}
	writeCtx, cancel := context.WithTimeout(ctx, WriteTimeout)
	defer cancel()
	if err := s.writer.Write(writeCtx, beat); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = at
	return nil
}

// wallClock reads the clock without its monotonic reading, which stops while the host sleeps.
func wallClock() time.Time {
	return time.Now().Round(0).UTC()
}
