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

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCaptureHooks_Validate(t *testing.T) {
	tests := []struct {
		name    string
		hooks   CaptureHooks
		wantErr string
	}{
		{
			name:  "a positive timeout, a known on-error",
			hooks: CaptureHooks{Timeout: time.Second, OnError: "continue"},
		},
		{
			name:    "a zero timeout",
			hooks:   CaptureHooks{OnError: "fail"},
			wantErr: "capture.hooks.timeout must be a positive duration, got 0s",
		},
		{
			name:    "a negative timeout",
			hooks:   CaptureHooks{Timeout: -time.Second, OnError: "fail"},
			wantErr: "capture.hooks.timeout must be a positive duration, got -1s",
		},
		{
			name:    "an unknown on-error",
			hooks:   CaptureHooks{Timeout: time.Second, OnError: "ignore"},
			wantErr: `unsupported capture.hooks.on_error "ignore" (supported: fail, continue)`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := tt.hooks.Validate()

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrInvalid)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestCDCBackend_IgnoredTCPKeys(t *testing.T) {
	tests := []struct {
		name string
		cdc  CDCBackend
		want []string
	}{
		{
			name: "stdio with no tcp key",
			cdc:  CDCBackend{Transport: CDCTransportStdio},
		},
		{
			name: "stdio with tcp keys set",
			cdc: CDCBackend{
				Transport:     CDCTransportStdio,
				ServerAddress: "127.0.0.1:0",
				NetworkMode:   NetworkModeBridge,
			},
			want: []string{"backends.cdc.server_address", "backends.cdc.network_mode"},
		},
		{
			name: "tcp reads them all",
			cdc: CDCBackend{
				Transport:     CDCTransportTCP,
				ServerAddress: "127.0.0.1:0",
				ServerScheme:  SchemeHTTP,
				ContainerHost: "host.docker.internal",
				NetworkMode:   NetworkModeBridge,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := tt.cdc.IgnoredTCPKeys()

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
