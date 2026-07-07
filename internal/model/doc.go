// Package model holds GM's shared domain types, passed between the repository,
// client, service, and adapter layers. It is the base of the dependency graph
// and imports no other internal package. Each type lives in its own file.
//
// Besides the core domain types (Mount, SkippedMount, Savepoint), the package
// also owns the JSON response shapes GM commands print to stdout: single-
// savepoint payloads marshal Savepoint directly (its json tags are the wire
// contract), while the multi-part envelopes — RollbackResponse, RemoveResponse,
// RestoredMount — have no other domain equivalent. Both the CLI and MCP adapters
// marshal these.
package model
