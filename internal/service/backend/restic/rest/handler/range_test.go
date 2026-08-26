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

package handler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseRangeHeader pins what a header says, not what it means against an
// object: no size appears here, and so neither does 416.
func TestParseRangeHeader(t *testing.T) {
	tests := []struct {
		name          string
		header        string
		want          rangeSpec
		wantNoneAsked bool
	}{
		{name: "no header", header: "", wantNoneAsked: true},
		{name: "a foreign unit", header: "items=0-1", wantNoneAsked: true},
		{name: "several ranges at once", header: "bytes=0-1,4-5", wantNoneAsked: true},
		{name: "no dash at all", header: "bytes=4", wantNoneAsked: true},
		{name: "a closed range", header: "bytes=0-3", want: rangeSpec{first: "0", last: "3"}},
		{name: "an open ended range", header: "bytes=10-", want: rangeSpec{first: "10", last: ""}},
		{name: "a suffix range", header: "bytes=-4", want: rangeSpec{first: "", last: "4"}},
		// Nothing here judges the numbers — that is parseRange's job, against a
		// size this function never sees.
		{name: "sides that are not numbers", header: "bytes=abc-def", want: rangeSpec{first: "abc", last: "def"}},
		{name: "surrounding space", header: " bytes= 0 - 3 ", want: rangeSpec{first: "0", last: "3"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			spec, askedForRange := parseRangeHeader(tt.header)

			// Assert
			assert.Equal(t, !tt.wantNoneAsked, askedForRange)
			assert.Equal(t, tt.want, spec)
		})
	}
}

// TestParseRange covers every branch of the one piece of HTTP semantics the handler
// implements itself. Each case is one expected value, the flag included.
func TestParseRange(t *testing.T) {
	const size = 16
	// whole is what every header this server does not read resolves to.
	whole := byteRange{start: 0, length: size, requestedWholeBlob: true}

	tests := []struct {
		name              string
		header            string
		size              int64
		want              byteRange
		wantUnsatisfiable bool
	}{
		{name: "no header is the whole object", header: "", size: size, want: whole},
		{name: "open ended from the start", header: "bytes=0-", size: size, want: byteRange{start: 0, length: 16}},
		{name: "closed range", header: "bytes=0-3", size: size, want: byteRange{start: 0, length: 4}},
		{name: "open ended from the middle", header: "bytes=10-", size: size, want: byteRange{start: 10, length: 6}},
		{name: "suffix range", header: "bytes=-4", size: size, want: byteRange{start: 12, length: 4}},
		{name: "suffix range longer than the object", header: "bytes=-99", size: size, want: byteRange{start: 0, length: 16}},
		{name: "end past the object is clamped", header: "bytes=12-99", size: size, want: byteRange{start: 12, length: 4}},
		{name: "the last byte", header: "bytes=15-15", size: size, want: byteRange{start: 15, length: 1}},
		{name: "start at the end is unsatisfiable", header: "bytes=16-20", size: size, wantUnsatisfiable: true},
		{name: "start past the end is unsatisfiable", header: "bytes=99-", size: size, wantUnsatisfiable: true},
		{name: "zero-length suffix is unsatisfiable", header: "bytes=-0", size: size, wantUnsatisfiable: true},
		{name: "any range over an empty object is unsatisfiable", header: "bytes=0-", size: 0, wantUnsatisfiable: true},
		{name: "an empty object with no header is not", header: "", size: 0, want: byteRange{requestedWholeBlob: true}},
		{name: "a malformed spec is ignored", header: "bytes=abc", size: size, want: whole},
		{name: "a foreign unit is ignored", header: "items=0-1", size: size, want: whole},
		{name: "several ranges are ignored", header: "bytes=0-1,4-5", size: size, want: whole},
		{name: "an end before its start is ignored", header: "bytes=9-4", size: size, want: whole},
		{name: "a negative start is ignored", header: "bytes=-4-9", size: size, want: whole},
		{name: "surrounding space is not", header: " bytes=0-3 ", size: size, want: byteRange{start: 0, length: 4}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := parseRange(tt.header, tt.size)

			// Assert
			if tt.wantUnsatisfiable {
				require.ErrorIs(t, err, errRangeNotSatisfiable)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestParseRange_WholeIsAWholeInterval pins the invariant the flag may not weaken: a
// request naming no part of the object still resolves to the interval covering it.
func TestParseRange_WholeIsAWholeInterval(t *testing.T) {
	// Act
	got, err := parseRange("", 16)

	// Assert
	require.NoError(t, err)
	assert.True(t, got.requestedWholeBlob)
	assert.Equal(t, int64(0), got.start)
	assert.Equal(t, int64(16), got.length)
}
