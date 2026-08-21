package model

import "time"

// CreateSavepointRequest carries everything CreateSavepoint needs, so a caller
// need not hand over a config.Config. The stack and the backend are injected.
type CreateSavepointRequest struct {
	Name        string            // savepoint name
	Description string            // savepoint description
	Labels      map[string]string // marks carried into the record and on to every backend
	WaitTimeout time.Duration     // bounds the health check on restart; <= 0 lets the compose runner apply its default
}
