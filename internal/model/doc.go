// Package model holds arcave's own domain types — what the client, the control
// plane and the provider seam all speak. It is the base of the dependency graph
// and imports no other internal package. Each type lives in its own file.
//
// What is here is runtime-neutral by construction: the id types that carry their
// own gate, the arcave project and its providers, Artifact and SkippedUnit — one
// captured unit as the record carries it, with the provider's own bookkeeping
// sealed inside Artifact.Spec, which arcave never parses — and RawSettings, a
// provider's config carried and never read. A runtime's vocabulary belongs to
// that runtime: docker's lives under internal/provider/docker.
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
