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

// Package model holds arcave's shared domain types, passed between the repository,
// client, service, and adapter layers. It is the base of the dependency graph
// and imports no other internal package. Each type lives in its own file.
//
// Besides the core domain types (Savepoint, Session), the package also owns the JSON response
// shapes arcave commands print to stdout: single-savepoint payloads marshal Savepoint directly
// (its json tags are the wire contract), while the multi-part envelopes — RollbackResult,
// RemoveResult — have no other domain equivalent. Both the CLI and MCP adapters marshal these.
// What a provider holds of a savepoint or a build is its own body, opaque here: arcave reads only
// the summary the provider derives from it.
//
// errors.go holds the sentinel errors that cross a layer seam: a service returns
// one and an adapter matches it with errors.Is, so the error is a contract
// between them and belongs with the other types they share.
//
// Every seam in arcave is declared by its consumer, per dependency inversion; the package declares
// only the factory-named seams the doctrine keeps here (SessionLocker, GrantedSessionLock) and
// GarbageStat, a wire value's method set. The storage contract is greenmaskio/storages' own
// Storager, consumed directly. A provider's own vocabulary lives in the provider: the compose
// provider's in its internal/model.
package model
