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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStatus_Validate(t *testing.T) {
	tests := []struct {
		status  Status
		wantErr bool
	}{
		{status: StatusInProgress},
		{status: StatusDone},
		{status: StatusFailed},
		{status: "", wantErr: true},
		{status: "Done", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			// Act
			err := tt.status.Validate()

			// Assert
			if tt.wantErr {
				assert.ErrorIs(t, err, errInvalidStatus)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
