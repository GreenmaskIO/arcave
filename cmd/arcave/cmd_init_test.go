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
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/storekey"
)

func TestNewInitTemplate(t *testing.T) {
	tests := []struct {
		name    string
		encrypt bool
		want    config.Encryption
	}{
		{name: "a plain store"},
		{name: "an encrypted store names the key's variable", encrypt: true, want: config.Encryption{Enabled: true, Key: config.KeyFromEnv}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newInitTemplate("demo", tt.encrypt)

			// Assert
			assert.Equal(t, "demo", got.Project.Name)
			assert.Equal(t, tt.want, got.Encryption)
			assert.NotContains(t, got.Encryption.Key, "AGE-SECRET-KEY")
		})
	}
}

func TestNewInitKeyNotice(t *testing.T) {
	key, err := storekey.NewKey()
	require.NoError(t, err)
	tests := []struct {
		name    string
		envKey  string
		wantErr error
		verify  func(t *testing.T, notice string)
	}{
		{
			name: "a key is generated while the variable is empty",
			verify: func(t *testing.T, notice string) {
				match := regexp.MustCompile(`export ARCAVE_ENCRYPTION_KEY=(\S+)`).FindStringSubmatch(notice)
				require.Len(t, match, 2)
				_, err := storekey.ParseKey(match[1])
				assert.NoError(t, err)
			},
		},
		{
			name:   "a key already set is only reminded of",
			envKey: key,
			verify: func(t *testing.T, notice string) {
				assert.Contains(t, notice, "the key in ARCAVE_ENCRYPTION_KEY")
				assert.NotContains(t, notice, key)
				assert.NotContains(t, notice, "export")
			},
		},
		{name: "a malformed key already set is refused", envKey: "AGE-SECRET-KEY-1NOTAKEY", wantErr: model.ErrInvalidStoreKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			notice, err := newInitKeyNotice(tt.envKey)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.ErrorContains(t, err, "ARCAVE_ENCRYPTION_KEY")
				return
			}
			require.NoError(t, err)
			tt.verify(t, notice)
		})
	}
}
