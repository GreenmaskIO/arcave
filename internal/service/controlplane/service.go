// Package controlplane is the ledger: savepoint identity, the record committing
// one, a session's history, retention, and the transaction around a provider call.
package controlplane

// Service is the ledger. It holds nothing yet.
type Service struct{}

// New builds the service.
func New() *Service { return &Service{} }
