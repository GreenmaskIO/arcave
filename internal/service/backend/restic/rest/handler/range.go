package handler

import (
	"errors"
	"strconv"
	"strings"
)

// bytesUnit is the only range unit this protocol ever sees.
const bytesUnit = "bytes="

// errRangeNotSatisfiable reports a range beginning past the end of the object,
// which RFC 9110 answers with a 416 rather than the whole representation.
var errRangeNotSatisfiable = errors.New("range not satisfiable")

// byteRange is the interval of an object a read is answered from.
type byteRange struct {
	start  int64
	length int64
	// requestedWholeBlob distinguishes the 200 from the 206: asking for all of an
	// object and asking for nothing in particular share an interval, not a status.
	requestedWholeBlob bool
}

func wholeBlobRange(size int64) byteRange {
	return byteRange{
		length:             size,
		requestedWholeBlob: true,
	}
}

type rangeSpec struct {
	first string
	last  string
}

// parseRangeHeader takes a Range header apart.
func parseRangeHeader(header string) (spec rangeSpec, askedForRange bool) {
	value, ok := strings.CutPrefix(strings.TrimSpace(header), bytesUnit)
	if !ok {
		return rangeSpec{}, false
	}
	if strings.Contains(value, ",") {
		return rangeSpec{}, false
	}
	first, last, ok := strings.Cut(strings.TrimSpace(value), "-")
	if !ok {
		return rangeSpec{}, false
	}
	return rangeSpec{first: strings.TrimSpace(first), last: strings.TrimSpace(last)}, true
}

// parseRange resolves the Range header of a request for an object of size
// bytes, into the interval to read and the status to read it under.
func parseRange(header string, size int64) (byteRange, error) {
	spec, askedForRange := parseRangeHeader(header)
	if !askedForRange {
		return wholeBlobRange(size), nil
	}
	// The suffix form, "the final N bytes": restic never sends one, but RFC 9110
	// defines it and it costs three lines.
	if spec.first == "" {
		n, err := strconv.ParseInt(spec.last, 10, 64)
		if err != nil || n < 0 {
			return wholeBlobRange(size), nil
		}
		if n == 0 || size == 0 {
			return byteRange{}, errRangeNotSatisfiable
		}
		return byteRange{
			start:  max(size-n, 0),
			length: min(n, size),
		}, nil
	}

	start, err := strconv.ParseInt(spec.first, 10, 64)
	if err != nil || start < 0 {
		return wholeBlobRange(size), nil
	}
	if start >= size {
		return byteRange{}, errRangeNotSatisfiable
	}

	// An open-ended range runs to the end of the object.
	if spec.last == "" {
		return byteRange{
			start:  start,
			length: size - start,
		}, nil
	}

	end, err := strconv.ParseInt(spec.last, 10, 64)
	if err != nil {
		return wholeBlobRange(size), nil
	}
	// An end preceding its start is an invalid header, not an unsatisfiable range.
	if end < start {
		return wholeBlobRange(size), nil
	}
	// Clamping the end is RFC 9110's own rule: the last byte position is the
	// lesser of the requested one and the final byte of the representation.
	return byteRange{
		start:  start,
		length: min(end, size-1) - start + 1,
	}, nil
}
