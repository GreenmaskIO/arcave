package model

type RecommendationOperation string

const (
	RecommendationOperationAddVolume RecommendationOperation = "add-volume"
)

// Recommendation is one change arcave advises on the compose project: service
// `Service`, running `Image`, should mount something persistent at `Volume`.
// Its json tags are the wire contract — `arcave recommend` marshals a slice of
// these directly.
//
// The rules that fill it live elsewhere: nothing outside service/core produces a
// recommendation. The operation vocabulary is a constant here because an adapter
// may dispatch on it; the hash is not — its canonical form is stated below and in
// doc/spec.md.
type Recommendation struct {
	Service string `json:"service"`
	Image   string `json:"image"`
	Volume  string `json:"volume"`
	// Operation names what to do about the finding. `"add-volume"` is the only
	// value arcave emits today; it is a field of its own so a future `arcave fix`
	// can dispatch on it without a change to the wire shape.
	Operation RecommendationOperation `json:"operation"`
	// Hash identifies the recommendation itself, not the run that produced it,
	// so a caller can suppress one it has already seen or acted on. The contract
	// is the lowercase hex sha256 of
	//
	//	service + "|" + image + "|" + volume + "|" + operation
	//
	// with Image the compose file's value verbatim, tag and all — bumping the tag
	// is meant to produce a new hash, since the advice may no longer apply. A test
	// in service/core pins the form against a literal.
	Hash string `json:"hash"`
}
