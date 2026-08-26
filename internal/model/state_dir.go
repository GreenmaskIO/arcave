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

// StateDir is the per-project directory arcave keeps all of its local state in,
// relative to the project root: the config file, the project lock, and the
// default savepoint store.
//
// It lives in model — the base every layer imports — so the config, lock, and
// safety-snapshot paths all render the same segment from one source. They were
// three independent literals once; renaming the tool then meant editing three
// files in lockstep, and missing one split the state silently across two
// directories. This is the single source; a future rename is a one-liner here.
const StateDir = ".arcave"
