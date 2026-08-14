package model

import "fmt"

// ProviderKind names which provider owns a stack: the config's vocabulary
// (`stack.provider`), and what a savepoint record is stamped with. The store
// layout is scoped by ProviderFamily instead — two providers of one family share
// a segment, and the record says which of them wrote a capture.
type ProviderKind string

// The provider kinds arcave knows. Only ProviderCompose is implemented.
const (
	// ProviderCompose is a docker-compose project, today's only stack provider.
	ProviderCompose ProviderKind = "compose"
	// ProviderUnknown is what an unrecognised token resolves to; the resolver
	// refuses it by name rather than dispatching on a zero value.
	ProviderUnknown ProviderKind = ""
)

// Target names which stack a request addresses: the provider that owns it, and
// that provider's own project.
//
// It replaces CreateSavepointRequest.ComposeFiles and RollbackRequest.ComposeFiles.
// A compose file list is provider lexicon, and a domain request must not carry
// it — which file set makes up a stack is the provider's business, resolved from
// its own config section. What arcave needs is only enough to find the provider
// and the tree its savepoints live in.
//
// Resolution is per request, not per process: a Target arrives with the call, so
// the same arcave can address stacks of two providers, or two daemons.
type Target struct {
	// Provider selects the provider. Empty means "the project's configured
	// default", which the resolver fills before it dispatches.
	Provider ProviderKind
	// Stack is the provider's own project — the compose project name for
	// ProviderCompose. It is the last segment of the store root, which is why it
	// carries ResourceName's gate rather than being a bare string.
	Stack ResourceName
}

// Validate accepts a target whose provider is implemented and whose stack name
// can become a path segment. An empty Provider is refused here rather than
// defaulted: filling the default is the resolver's job, and a zero value that
// reached this far means nobody did it.
func (t Target) Validate() error {
	switch t.Provider {
	case ProviderCompose:
	case ProviderUnknown:
		return fmt.Errorf("no stack provider named")
	default:
		return fmt.Errorf("unsupported stack provider %q (supported: %s)", t.Provider, ProviderCompose)
	}
	return t.Stack.Validate()
}

// String renders the target the way logs and errors name it.
func (t Target) String() string { return string(t.Provider) + "/" + t.Stack.String() }
