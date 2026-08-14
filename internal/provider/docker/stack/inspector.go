package stack

import (
	"context"

	stackmodel "github.com/greenmaskio/arcave/internal/provider/docker/stack/model"
)

// Inspector is the reading half of the provider — the layout of the stack as
// declared, which `arcave recommend` and a future `arcave status` read.
//
// Not part of the arcave seam: no savepoint workflow calls it, and what it
// answers with is this provider's own vocabulary. A read-only command must not
// have to build a capture runtime to answer a question about a file.
type Inspector interface {
	Introspect(ctx context.Context) (stackmodel.IntrospectionResult, error)
}
