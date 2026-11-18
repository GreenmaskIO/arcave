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

package utils

import (
	"context"
	"io"
	"slices"

	"github.com/rs/zerolog/log"
)

// Closers is what a wiring has opened, released newest first.
type Closers struct {
	releases []func(ctx context.Context)
}

// Add puts closer on the stack; a failure to close it is logged under name.
func (c *Closers) Add(name string, closer io.Closer) {
	c.AddFunc(func(ctx context.Context) { CloseQuietly(ctx, name, closer) })
}

// AddFunc puts a release that reports its own failure on the stack.
func (c *Closers) AddFunc(fn func(ctx context.Context)) {
	c.releases = append(c.releases, fn)
}

// Len counts the releases on the stack.
func (c *Closers) Len() int {
	return len(c.releases)
}

// Release runs the stack newest first.
func (c *Closers) Release(ctx context.Context) {
	for _, release := range slices.Backward(c.releases) {
		release(ctx)
	}
}

// CloseQuietly closes closer and logs any error without failing the caller.
func CloseQuietly(ctx context.Context, name string, closer io.Closer) {
	if err := closer.Close(); err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("close " + name)
	}
}
