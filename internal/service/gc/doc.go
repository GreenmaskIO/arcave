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

// Package gc finds a session's garbage and deletes it: GarbageCollector reads every compose
// project's scope once and runs the collectors over it. The collectors, one per kind of garbage,
// live under collector/ and are handed in by the composition root; each judges liveness by
// savepoint status, never by an object's age, and only finds: the GarbageEraser of each op's
// action deletes it.
package gc
