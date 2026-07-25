// Package model holds arcave's shared domain types, passed between the repository,
// client, service, and adapter layers. It is the base of the dependency graph
// and imports no other internal package. Each type lives in its own file.
//
// Besides the core domain types (Mount, SkippedMount, Savepoint), the package
// also owns the JSON response shapes arcave commands print to stdout: single-
// savepoint payloads marshal Savepoint directly (its json tags are the wire
// contract), while the multi-part envelopes — RollbackResponse, RemoveResponse,
// RestoredMount — have no other domain equivalent. Both the CLI and MCP adapters
// marshal these.
//
// errors.go holds the sentinel errors that cross a layer seam: a service returns
// one and an adapter matches it with errors.Is, so the error is a contract
// between them and belongs with the other types they share.
//
// The package declares no interfaces: every seam in arcave is declared by its
// consumer, per dependency inversion. The storage contract is the exception that
// proves it — it is greenmaskio/storages' own Storager, consumed directly, so
// there is nothing here to re-declare.
package model
