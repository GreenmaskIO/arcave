// Package model is the compose provider's own vocabulary: the shape of a
// compose project as arcave reads it, and the recommender catalog entry.
//
// Nothing here crosses the arcave seam. What the docker runtime's two families
// share lives in provider/docker/internal/model, and what arcave itself carries
// lives in internal/model — this package imports the first, and no consumer of
// it belongs outside provider/docker/stack.
package model
