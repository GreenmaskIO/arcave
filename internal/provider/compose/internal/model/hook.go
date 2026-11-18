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
	"time"

	"github.com/greenmaskio/arcave/internal/model"
)

// Hook is one command run in each replica of a service, its defaults applied.
type Hook struct {
	// Command is the argv, run with no shell.
	Command []string
	// User runs it; empty is the container's user.
	User    string
	Timeout time.Duration
	OnError model.HookOnError
}
