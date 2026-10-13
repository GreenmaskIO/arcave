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

package heartbeat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// Reader reads the heartbeat at the root of one tree's store.
type Reader struct {
	st         storages.Storager
	staleAfter time.Duration
}

// NewReader returns a reader of st's heartbeat, stale after DefaultStaleAfter.
func NewReader(st storages.Storager) *Reader {
	return &Reader{st: st, staleAfter: DefaultStaleAfter}
}

// SetStaleAfter sets how long an in-progress heartbeat stays alive unrewritten.
func (r *Reader) SetStaleAfter(v time.Duration) *Reader {
	r.staleAfter = v
	return r
}

// Read returns the heartbeat with its status as of now, aged by the store's LastModified: an
// in-progress one gone stale reads as failed. None is model.ErrHeartbeatNotFound.
func (r *Reader) Read(ctx context.Context) (Heartbeat, error) {
	lastModified, err := r.lastModified()
	if err != nil {
		return Heartbeat{}, err
	}
	h, err := r.decode(ctx)
	if err != nil {
		return Heartbeat{}, err
	}
	if h.Status, err = h.StatusAt(lastModified, time.Now(), r.staleAfter); err != nil {
		return Heartbeat{}, fmt.Errorf("read heartbeat: %w", err)
	}
	return h, nil
}

// lastModified is when the store last wrote the heartbeat.
func (r *Reader) lastModified() (time.Time, error) {
	stat, err := r.st.Stat(FileName)
	if err != nil {
		return time.Time{}, fmt.Errorf("stat heartbeat: %w", err)
	}
	if !stat.Exist {
		return time.Time{}, model.ErrHeartbeatNotFound
	}
	return stat.LastModified, nil
}

func (r *Reader) decode(ctx context.Context) (Heartbeat, error) {
	obj, err := r.st.GetObject(ctx, FileName)
	if errors.Is(err, storages.ErrFileNotFound) {
		// Deleted since it was stat'ed.
		return Heartbeat{}, model.ErrHeartbeatNotFound
	}
	if err != nil {
		return Heartbeat{}, fmt.Errorf("get heartbeat: %w", err)
	}
	defer func() {
		if err := obj.Close(); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("closing the heartbeat")
		}
	}()
	var h Heartbeat
	if err := json.NewDecoder(obj).Decode(&h); err != nil {
		return Heartbeat{}, fmt.Errorf("decode heartbeat: %w", err)
	}
	return h, nil
}
