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

package main

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/utils"
)

func TestNewRollbackSafetySnapshotSwitch(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    *bool
		wantErr bool
	}{
		{name: "neither flag leaves the config to decide"},
		{name: "--safety-snapshot takes one", args: []string{"--safety-snapshot"}, want: utils.New(true)},
		{name: "--no-safety-snapshot takes none", args: []string{"--no-safety-snapshot"}, want: utils.New(false)},
		{name: "a value is refused", args: []string{"--no-safety-snapshot=false"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			t.Cleanup(func() { rollbackSafetySnapshot = nil })
			fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
			fs.BoolFunc("safety-snapshot", "", newRollbackSafetySnapshotSwitch(true))
			fs.BoolFunc("no-safety-snapshot", "", newRollbackSafetySnapshotSwitch(false))

			// Act
			err := fs.Parse(tt.args)

			// Assert
			if tt.wantErr {
				require.ErrorIs(t, err, errSwitchTakesNoValue)
				assert.Nil(t, rollbackSafetySnapshot)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, rollbackSafetySnapshot)
		})
	}
}
