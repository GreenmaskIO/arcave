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

import "time"

// CreateSavepointRequest carries everything CreateSavepoint needs, so a caller
// need not hand over a config.Config. The compose project and the backend are injected.
type CreateSavepointRequest struct {
	Name        string            // savepoint name
	Description string            // savepoint description
	Labels      map[string]string // marks carried into the record and on to every backend
	WaitTimeout time.Duration     // bounds the health check on restart; <= 0 lets the compose runner apply its default
}
