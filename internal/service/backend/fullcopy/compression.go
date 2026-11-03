// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fullcopy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"

	"github.com/klauspost/compress/zstd"
)

// The formats a capture's archive is stored in.
const (
	// CompressionNone stores the tar stream as the daemon hands it over.
	CompressionNone Compression = "none"
	// CompressionZstd stores it under zstd at its default level, zstd -3's.
	CompressionZstd Compression = "zstd"
)

// The keys an archive is stored under, one per format.
const (
	archiveNameTar  = "data.tar"
	archiveNameZstd = "data.tar.zst"
)

// errUnknownCompression refuses a format this backend cannot read or write.
var errUnknownCompression = errors.New("unknown fullcopy compression")

// Compression is the format a capture stores its tar stream in.
type Compression string

// Validate refuses a format this backend has no codec for.
func (c Compression) Validate() error {
	switch c {
	case CompressionNone, CompressionZstd:
		return nil
	default:
		return fmt.Errorf("%w %q (supported: %s, %s)", errUnknownCompression, c, CompressionNone, CompressionZstd)
	}
}

// archiveName is the key the archive of this format is stored under.
func (c Compression) archiveName() string {
	if c == CompressionZstd {
		return archiveNameZstd
	}
	return archiveNameTar
}

// Payload is what a fullcopy capture records: the format its archive is stored
// in, which a restore and a remove read the key and the decoder by.
type Payload struct {
	Compression Compression `json:"compression"`
}

// DecodePayload reads back what a capture reported — a map[string]any once it
// has been through the store. No payload, or one naming no format, is CompressionNone.
func DecodePayload(stored any) (Payload, error) {
	var payload Payload
	if stored != nil {
		raw, err := json.Marshal(stored)
		if err != nil {
			return Payload{}, fmt.Errorf("re-encode fullcopy payload: %w", err)
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return Payload{}, fmt.Errorf("decode fullcopy payload: %w", err)
		}
	}
	if payload.Compression == "" {
		payload.Compression = CompressionNone
	}
	if err := payload.Compression.Validate(); err != nil {
		return Payload{}, err
	}
	return payload, nil
}

// newEncoder wraps w in the format's encoder; Close flushes it and leaves w open.
func newEncoder(compression Compression, w io.Writer) (io.WriteCloser, error) {
	if compression == CompressionNone {
		return nopWriteCloser{Writer: w}, nil
	}
	encoder, err := zstd.NewWriter(w,
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithEncoderConcurrency(runtime.GOMAXPROCS(0)),
	)
	if err != nil {
		return nil, fmt.Errorf("open zstd encoder: %w", err)
	}
	return encoder, nil
}

// newDecoder wraps r in the format's decoder; Close releases it and leaves r open.
func newDecoder(compression Compression, r io.Reader) (io.ReadCloser, error) {
	if compression == CompressionNone {
		return io.NopCloser(r), nil
	}
	decoder, err := zstd.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("open zstd decoder: %w", err)
	}
	return decoder.IOReadCloser(), nil
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
