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

package docs

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSpec pins that the embedded document survived the build and the
// conversion: what is served is what the api package was generated from, and an
// empty Spec would otherwise only show up in a browser.
func TestSpec(t *testing.T) {
	// Act
	var document map[string]any
	err := json.Unmarshal([]byte(Spec), &document)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "3.0.3", document["openapi"])
	assert.Len(t, document["paths"], 4, "the four paths of the protocol")
}

// TestDocReader_ReadDoc pins the one method swag's registry reads the document
// through.
func TestDocReader_ReadDoc(t *testing.T) {
	// Act
	got := (&DocReader{}).ReadDoc()

	// Assert
	assert.Equal(t, Spec, got)
}
