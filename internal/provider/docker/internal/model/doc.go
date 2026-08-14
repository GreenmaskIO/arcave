// Package model is the vocabulary the docker runtime's provider families share:
// the unit of backup and how it is addressed, the capture-backend seam and its
// requests, the one-shot container spec, the interrupted-rollback marker, and
// the savepoint record as this runtime still writes it.
//
// It sits below internal/provider/docker/internal, so nothing outside
// provider/docker can name any of it — which is the point: none of it is
// arcave's business. It imports internal/model (as arcavemodel) for what is:
// the id gates, the artifact shape, the safety-snapshot label.
//
// Compose's own vocabulary is one level further down again, in
// provider/docker/stack/model.
package model
