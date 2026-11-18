// Package fanout turns a configured concurrency into an errgroup limit.
package fanout

// Limit turns a configured concurrency into an errgroup limit, where a
// negative one is unbounded and zero would admit no goroutine at all.
func Limit(concurrency int) int {
	if concurrency < 1 {
		return -1
	}
	return concurrency
}
