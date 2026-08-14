package model

import "fmt"

// ProviderFamily is what a provider drives: a stack of long-lived services, or
// a sandbox. It is a segment of every store root, which is why it carries a gate
// rather than being a bare string.
type ProviderFamily string

// The families arcave registers. Only ProviderFamilyStack has an implementation.
const (
	ProviderFamilyStack   ProviderFamily = "stack"
	ProviderFamilySandbox ProviderFamily = "sandbox"
)

// Validate accepts a known family. An allowlist, like every other segment: a
// list of what a directory name must not be is never provably complete.
func (m ProviderFamily) Validate() error {
	switch m {
	case ProviderFamilyStack, ProviderFamilySandbox:
		return nil
	default:
		return fmt.Errorf("invalid provider family %q (supported: %s, %s)",
			string(m), ProviderFamilyStack, ProviderFamilySandbox)
	}
}

// String renders the family as the path segment it becomes.
func (m ProviderFamily) String() string { return string(m) }
