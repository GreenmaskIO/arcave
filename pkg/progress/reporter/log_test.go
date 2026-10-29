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

package reporter

import (
	"bytes"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/pkg/progress"
)

func TestLog_Report(t *testing.T) {
	var out bytes.Buffer
	reporter := NewLog(zerolog.New(zerolog.ConsoleWriter{
		Out:          &out,
		NoColor:      true,
		PartsExclude: []string{zerolog.TimestampFieldName},
	}))

	for _, evt := range newStream() {
		reporter.Report(evt)
	}

	assertGolden(t, "stream.log", out.Bytes())
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name  string
		bytes progress.Bytes
		want  string
	}{
		{name: "known total", bytes: progress.Bytes{Done: 1_200_000, Total: 2_100_000}, want: "1.2MB/2.1MB"},
		{name: "unknown total", bytes: progress.Bytes{Done: 1_200_000}, want: "1.2MB"},
		{name: "exceeded total", bytes: progress.Bytes{Done: 2_500_000, Total: 2_100_000}, want: "2.5MB"},
		{name: "under a kilobyte", bytes: progress.Bytes{Done: 512, Total: 999}, want: "512B/999B"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatBytes(tt.bytes))
		})
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		size int64
		want string
	}{
		{size: 999, want: "999B"},
		{size: 1000, want: "1.0KB"},
		{size: 420_000, want: "420KB"},
		{size: 999_499, want: "999KB"},
		{size: 999_500, want: "1.0MB"},
		{size: 9_949_999, want: "9.9MB"},
		{size: 9_950_000, want: "10MB"},
		{size: 24_000_000, want: "24MB"},
		{size: 2_000_000_000_000_000_000, want: "2000PB"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, formatSize(tt.size))
		})
	}
}

func TestFormatItems(t *testing.T) {
	tests := []struct {
		name  string
		items progress.Items
		want  string
	}{
		{name: "known total", items: progress.Items{Done: 3, Failed: 1, Total: 5}, want: "3/5"},
		{name: "unknown total", items: progress.Items{Done: 3}, want: "3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatItems(tt.items))
		})
	}
}

func TestFormatPath(t *testing.T) {
	tests := []struct {
		name   string
		parent string
		step   progress.Step
		want   string
	}{
		{name: "root", step: progress.Step{Kind: "album.publish", Name: "summer"}, want: "album.publish[summer]"},
		{name: "nested", parent: "album.publish", step: progress.Step{Kind: "album.export"}, want: "album.publish/album.export"},
		{name: "id alone", step: progress.Step{Kind: "photo", ID: "beach.jpg"}, want: "photo[beach.jpg]"},
		{name: "the name before the id", step: progress.Step{Kind: "photo", ID: "p-1", Name: "beach.jpg"}, want: "photo[beach.jpg]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatPath(tt.parent, tt.step))
		})
	}
}
