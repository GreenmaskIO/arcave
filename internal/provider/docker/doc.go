// Package docker holds every provider that drives a docker daemon, and the
// machinery they share under internal/.
//
// The `docker` segment is the *runtime*, not the family: model.ProviderFamily is
// `stack` or `sandbox`. So docker/stack is the stack family on the docker
// runtime, whose model.ProviderKind is `compose`, and docker/sandbox will be the
// sandbox family on the same runtime.
package docker
