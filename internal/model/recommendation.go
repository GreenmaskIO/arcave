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
// The type is here because both the CLI and a future MCP adapter marshal it. The
// rules that fill it are not: nothing outside service/core mints a
// recommendation, so neither the hash nor the operation vocabulary is a Go
// constant here — they are stated below and in doc/spec.md, which is what an
// external consumer of the JSON actually reads.
type Recommendation struct {
	Service   string                  `json:"service"`
	Image     string                  `json:"image"`
	Volume    string                  `json:"volume"`
	Operation RecommendationOperation `json:"operation"`
	// Hash identifies the recommendation itself, not the run that produced it,
	// so a caller can suppress one it has already seen or acted on. The contract
	// is the lowercase hex sha256 of
	//
	//	service + "|" + image + "|" + volume + "|" + operation
	//
	// with Image the compose file's value verbatim, tag and all — bumping the
	// image tag is meant to produce a new hash, since the advice may no longer
	// apply. service/core mints it, and a test there pins this canonical form
	// against a literal, because every hash a user has stored depends on it.
	Hash string `json:"hash"`
}
