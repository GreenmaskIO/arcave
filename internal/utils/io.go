package utils

import "io"

// CountingReader tallies the bytes read through it, so a stream handed to a writer
// that reports no size can still say how much of it went by.
type CountingReader struct {
	r io.Reader
	n int64
}

// NewCountingReader puts a counter in front of r.
func NewCountingReader(r io.Reader) *CountingReader {
	return &CountingReader{r: r}
}

func (c *CountingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// N is the number of bytes read so far.
func (c *CountingReader) N() int64 { return c.n }
