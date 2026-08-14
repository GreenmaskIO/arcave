package stack

import (
	inflightrepo "github.com/greenmaskio/arcave/internal/provider/docker/internal/repository/inflight"
)

// newInflightMarker builds the host-local marker an interrupted rollback leaves behind.
func (p *Provider) newInflightMarker(projectRoot string) *inflightrepo.Repository {
	return inflightrepo.New(projectRoot)
}
