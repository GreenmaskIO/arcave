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
//
// errors.go holds the sentinel errors that cross a layer seam: a service returns
// one and an adapter matches it with errors.Is, so the error is a contract
// between them and belongs with the other types they share.
//
// The package also owns the only two interfaces that live outside the package
// consuming them: Storager (storager.go) and Printer (printer.go). Every other
// seam in GM is declared by its consumer, per dependency inversion. These two
// cannot be, because each has several consumers *and* a factory that must name
// it in a signature — storages.New returns a Storager, and cmd/gm's dependencies
// holds a Printer field. A consumer-declared interface works as a parameter but
// never as a return type or an exported field, so the contract lives here, where
// every side already looks.
package model
