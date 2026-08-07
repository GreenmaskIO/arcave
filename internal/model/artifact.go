package model

// Artifact is one unit a capture covered, as the savepoint record carries it.
//
// arcave stores it, hands it back to Restore and Discard, and renders it — but
// never parses Spec and never branches on Kind. That is what keeps the record
// free of any one runtime's vocabulary: a docker volume, a bind mount, an image
// layer and a PVC are all one shape here, and the difference lives in Spec,
// which only the provider that wrote it decodes.
type Artifact struct {
	// ID addresses the unit inside the provider's own name space. arcave uses it
	// as a stable key for rendering and for diffing two savepoints, nothing else.
	ID string `json:"id"`
	// Kind is the provider's word for what this unit is. It is the tag Spec is
	// decoded by, on the provider's side.
	Kind string `json:"kind"`
	// Spec is the provider's own bookkeeping. It must be JSON-round-trippable: it
	// goes through savepoint.json, and it will go over a wire the day the provider
	// is a service — so a decoder must accept map[string]any as well as its own
	// type.
	Spec any `json:"spec,omitempty"`
}

// SkippedUnit is a unit the provider deliberately left out, with the reason.
// Artifact's shape minus the payload: there is nothing to store, only something
// to say.
//
// It is part of the record rather than a log line, because "what this savepoint
// does not cover" is a promise arcave makes to whoever restores it.
type SkippedUnit struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}
