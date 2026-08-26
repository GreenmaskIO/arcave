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

package repo

import (
	"context"
	"strings"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
)

// blobID is a syntactically valid object name: exactly 64 lowercase hex
// characters. The repository itself never judges the shape — a request carries
// an address already parsed — but the tests read better with a name of the real
// form.
const blobID = model.ObjectID("1111111111111111111111111111111111111111111111111111111111111111")

// otherBlobID is a second valid name, for the cases that need two.
const otherBlobID = model.ObjectID("2222222222222222222222222222222222222222222222222222222222222222")

// newRepository returns the repository over a fresh directory store, plus that
// store so a test can plant objects behind the repository's back and read what
// it wrote.
func newRepository(t *testing.T) (*Repository, storages.Storager) {
	t.Helper()

	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	return New(store), store
}

// put plants an object directly in the store, behind the repository's back.
func put(t *testing.T, store storages.Storager, key, body string) {
	t.Helper()

	require.NoError(t, store.PutObject(context.Background(), key, strings.NewReader(body)))
}
