package model

import "time"

// RollbackRequest carries everything Rollback needs, so callers (CLI, MCP,
// other services) can invoke it without handing over a whole config.Config.
type RollbackRequest struct {
	ComposeFiles []string      // compose files enumerating the services and their mounts
	Ref          string        // savepoint selector: latest | <id> | <name>
	WaitTimeout  time.Duration // bounds the health check on restart
}
