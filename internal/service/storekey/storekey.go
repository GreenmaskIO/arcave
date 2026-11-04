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

// Package storekey parses the store key encryption.key names and admits an arcave project's
// store for it.
package storekey

import (
	"fmt"

	"filippo.io/age"

	"github.com/greenmaskio/arcave/internal/model"
)

// ParseKey parses a store key; none, or a malformed one, is model.ErrInvalidStoreKey, its
// text left out.
func ParseKey(key string) (*age.X25519Identity, error) {
	if key == "" {
		return nil, fmt.Errorf("%w: none set", model.ErrInvalidStoreKey)
	}
	identity, err := age.ParseX25519Identity(key)
	if err != nil {
		return nil, fmt.Errorf("%w: not an age X25519 identity", model.ErrInvalidStoreKey)
	}
	return identity, nil
}
