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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A request must name a savepoint, and a pointer set to the zero value does not:
// an empty id or name selects nothing, and Latest=false says "not the latest".
// Every accepted request is one findSavepoint can branch on by nil-ness alone,
// so this is the gate that makes those branches sound.
func TestRollbackRequest_Validate(t *testing.T) {
	tests := []struct {
		name       string
		req        RollbackRequest
		wantErr    bool
		wantErrMsg string // set for an error other than ErrRollbackNoSelector
	}{
		{name: "an id", req: RollbackRequest{ID: ptr(SavepointID("019e843e-1650-7000-8000-000000000000"))}},
		{name: "a name", req: RollbackRequest{Name: ptr("before-migration")}},
		{name: "latest", req: RollbackRequest{Latest: ptr(true)}},
		{name: "nothing at all", req: RollbackRequest{}, wantErr: true},
		{name: "an empty id", req: RollbackRequest{ID: ptr(SavepointID(""))}, wantErr: true},
		{name: "an empty name", req: RollbackRequest{Name: ptr("")}, wantErr: true},
		{name: "latest set to false", req: RollbackRequest{Latest: ptr(false)}, wantErr: true},
		// The bound alone is not a selector: every request carries one.
		{name: "a wait timeout alone", req: RollbackRequest{WaitTimeout: time.Minute}, wantErr: true},
		// An empty id beside a real name still validates — the empty one is simply
		// not a selector, and the name is.
		{name: "an empty id beside a name", req: RollbackRequest{ID: ptr(SavepointID("")), Name: ptr("sp")}},
		{name: "a refusing policy", req: RollbackRequest{Latest: ptr(true), LeftoverVolumes: LeftoverVolumesRefuse}},
		{
			name:       "an unknown policy",
			req:        RollbackRequest{Latest: ptr(true), LeftoverVolumes: "keep"},
			wantErrMsg: `unsupported leftover volumes policy "keep"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := tt.req.Validate()

			// Assert
			if tt.wantErrMsg != "" {
				assert.EqualError(t, err, tt.wantErrMsg)
				return
			}
			if tt.wantErr {
				assert.ErrorIs(t, err, ErrRollbackNoSelector)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// Ref renders the selector back as the word a user would have typed: it is what
// error messages quote and what a safety snapshot carries in its name. Ids win,
// being the least ambiguous thing a caller can say.
func TestRollbackRequest_Ref(t *testing.T) {
	tests := []struct {
		name string
		req  RollbackRequest
		want string
	}{
		{name: "an id", req: RollbackRequest{ID: ptr(SavepointID("019e843e-1650-7000-8000-000000000000"))}, want: "019e843e-1650-7000-8000-000000000000"},
		{name: "a name", req: RollbackRequest{Name: ptr("before-migration")}, want: "before-migration"},
		{name: "latest", req: RollbackRequest{Latest: ptr(true)}, want: RefLatest},
		{name: "latest set to false renders nothing", req: RollbackRequest{Latest: ptr(false)}},
		{name: "nothing at all renders nothing", req: RollbackRequest{}},
		{
			name: "an id beats the two below it",
			req:  RollbackRequest{ID: ptr(SavepointID("100")), Name: ptr("sp"), Latest: ptr(true)},
			want: "100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := tt.req.Ref()

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// The reserved word is a contract both adapters spell, so it is pinned here
// rather than left to whichever of them is read first.
func TestRefLatest(t *testing.T) {
	require.Equal(t, "latest", RefLatest)
}

func ptr[T any](v T) *T { return &v }
