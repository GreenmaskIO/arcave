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
	"fmt"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
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

// Read returns the heartbeat and the status it stands for now, aged by the store's
// LastModified; found is false when there is none.
func (r *Reader) Read(ctx context.Context) (h Heartbeat, status Status, found bool, err error) {
	lastModified, found, err := r.lastModified()
	if err != nil || !found {
		return Heartbeat{}, "", false, err
	}
	h, err = r.decode(ctx)
	if err != nil {
		return Heartbeat{}, "", false, err
	}
	status, err = h.StatusAt(lastModified, time.Now(), r.staleAfter)
	if err != nil {
		return Heartbeat{}, "", false, fmt.Errorf("read heartbeat: %w", err)
	}
	return h, status, true, nil
}

// lastModified is when the store last wrote the heartbeat.
func (r *Reader) lastModified() (time.Time, bool, error) {
	stat, err := r.st.Stat(FileName)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("stat heartbeat: %w", err)
	}
	if !stat.Exist {
		return time.Time{}, false, nil
	}
	return stat.LastModified, true, nil
}

func (r *Reader) decode(ctx context.Context) (Heartbeat, error) {
	obj, err := r.st.GetObject(ctx, FileName)
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
