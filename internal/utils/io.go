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

import "io"

// CountingReader tallies the bytes read through it, so a stream handed to a writer
// that reports no size can still say how much of it went by.
type CountingReader struct {
	r io.Reader
	n int64
}

// NewCountingReader puts a counter in front of r.
func NewCountingReader(r io.Reader) *CountingReader {
	return &CountingReader{r: r}
}

func (c *CountingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// N is the number of bytes read so far.
func (c *CountingReader) N() int64 { return c.n }
