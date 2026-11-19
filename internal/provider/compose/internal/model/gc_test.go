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

package model

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
)

// A restic stat's bytes are its estimates', never its ops': a forget alone frees nothing.
func TestResticRepositoryStat_Summary(t *testing.T) {
	size := func(n int64) *int64 { return &n }
	tests := []struct {
		name string
		stat ResticRepositoryStat
		want model.GarbageSummary
	}{
		{
			name: "no estimate frees nothing",
			stat: ResticRepositoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}},
			want: model.GarbageSummary{Bytes: size(0)},
		},
		{
			name: "estimates add up",
			stat: ResticRepositoryStat{
				GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{{}, {}}, Unjudged: []model.GarbageSkip{{}}},
				Estimates:       []ResticForgetEstimate{{FreedBytes: size(3)}, {FreedBytes: size(4)}},
			},
			want: model.GarbageSummary{Found: 2, Bytes: size(7), Unjudged: 1},
		},
		{
			name: "one unknown estimate leaves the total unknown",
			stat: ResticRepositoryStat{
				GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{{}, {}}},
				Estimates:       []ResticForgetEstimate{{FreedBytes: size(3)}, {}},
			},
			want: model.GarbageSummary{Found: 2},
		},
		{
			name: "an op's own bytes are not counted",
			stat: ResticRepositoryStat{
				GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{{Bytes: size(100)}}},
				Estimates:       []ResticForgetEstimate{{FreedBytes: size(5)}},
			},
			want: model.GarbageSummary{Found: 1, Bytes: size(5)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.stat.Summary())
		})
	}
}
