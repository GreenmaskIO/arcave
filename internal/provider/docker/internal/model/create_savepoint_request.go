package model

import (
	"time"

	arcavemodel "github.com/greenmaskio/arcave/internal/model"
)

// CreateSavepointRequest carries everything CreateSavepoint needs, so callers
// (CLI, MCP, other services) can invoke it without handing over a whole
// config.Config. Where the snapshot data lands is bound to the injected snapshot
// backend, so it is deliberately absent here.
type CreateSavepointRequest struct {
	// Target names the stack to capture: which provider owns it, and that
	// provider's own project. It is what replaces ComposeFiles below — a compose
	// file list is provider lexicon, and which files make up a stack is resolved
	// from the provider's own config section, not carried in a domain request.
	//
	// A pointer so a caller that names nothing gets the project's configured
	// default, filled by the resolver.
	Target *arcavemodel.Target

	Name        string            // savepoint name
	Description string            // savepoint description
	Labels      map[string]string // marks carried into the record and on to every backend

	// ComposeFiles and WaitTimeout are the pre-provider-cut fields, still read by
	// the runtime's internal/core. They are superseded by Target and by the provider's
	// own config, and go away with the increment that retires core.
	ComposeFiles []string
	WaitTimeout  time.Duration
}
