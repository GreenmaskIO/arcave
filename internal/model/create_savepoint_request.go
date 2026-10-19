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
	"fmt"
	"strings"
	"time"
)

// CreateSavepointRequest carries everything CreateSavepoint needs, so a caller
// need not hand over a config.Config. The compose project and the backend are injected.
type CreateSavepointRequest struct {
	Name        string            // savepoint name
	Description string            // savepoint description
	Labels      map[string]string // marks carried into the record and on to every backend
	WaitTimeout time.Duration     // bounds the health check on restart; <= 0 lets the compose runner apply its default
}

// Validate refuses a name carrying "/", which a <session>/<savepoint> address splits on,
// and one rollback would read as another ref: RefLatest or a savepoint id.
func (m CreateSavepointRequest) Validate() error {
	switch {
	case strings.Contains(m.Name, "/"):
		return fmt.Errorf("%w %q: must not contain \"/\"", ErrInvalidSavepointName, m.Name)
	case m.Name == RefLatest:
		return fmt.Errorf("%w %q: is reserved", ErrInvalidSavepointName, m.Name)
	case m.Name != "" && SavepointID(m.Name).Validate() == nil:
		return fmt.Errorf("%w %q: must not be a savepoint id", ErrInvalidSavepointName, m.Name)
	}
	return nil
}
