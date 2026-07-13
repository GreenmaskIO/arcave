package model

// CreateSavepointRequest carries everything CreateSavepoint needs, so callers
// (CLI, MCP, other services) can invoke it without handing over a whole
// config.Config. Where the snapshot data lands is bound to the injected snapshot
// backend, so it is deliberately absent here.
type CreateSavepointRequest struct {
	ComposeFiles []string // compose files enumerating the services and their mounts
	Name         string   // savepoint name
	Description  string   // savepoint description
	WaitTimeout  int      // bounds the health check on restart; <= 0 lets the compose runner apply its default
}
