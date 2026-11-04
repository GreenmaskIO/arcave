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
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeys(t *testing.T) {
	// Act
	keys := Keys()

	// Assert
	assert.True(t, slices.IsSorted(keys), "sorted")
	assert.Equal(t, slices.Compact(slices.Clone(keys)), keys, "no duplicates")
	for _, leaf := range []string{
		"project.name",
		"storage.s3.bucket",         // a storage arm, behind a pointer
		"backends.cdc.restore_mode", // a section behind a pointer
		"capture.volumes.bind",      // a *bool leaf
		"compose.wait_timeout",      // a duration leaf
	} {
		assert.Contains(t, keys, leaf)
	}
	for _, section := range []string{"storage", "storage.s3", "backends.cdc", "capture.volumes"} {
		assert.NotContains(t, keys, section, "a section is no key")
	}
}

// A key Keys cannot name is one no variable can set.
func TestNewKeys_PanicsOnAnUntaggedField(t *testing.T) {
	// Arrange
	type untagged struct{ Name string }

	// Act & Assert
	assert.PanicsWithValue(t, "config key of untagged.Name: no mapstructure tag", func() {
		newKeys(reflect.TypeFor[untagged](), "")
	})
}
