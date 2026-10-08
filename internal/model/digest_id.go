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

package model

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ImageDigestID identifies image content by the bytes it is made of — "sha256:" and 64
// hex digits, the form a commit answers with. A reference is not one, so asking
// the daemon about a ImageDigestID is an identity check and can never resolve to
// whatever a tag happens to name today.
type ImageDigestID string

// ParseImageDigestID is the one door a string becomes an id through. It gates the form
// here, where the daemon's answer arrives, rather than leaving a tag to be
// resolved into a different image somewhere downstream.
func ParseImageDigestID(s string) (ImageDigestID, error) {
	const (
		// digestAlgorithmPrefix is the one algorithm docker identifies image content by.
		digestAlgorithmPrefix = "sha256:"
		// digestHexLen is how long a sha256 digest is once written in hex.
		digestHexLen = 64
	)

	hex, ok := strings.CutPrefix(s, digestAlgorithmPrefix)
	if !ok {
		return "", fmt.Errorf("%w %q: must open %q", ErrInvalidDigestID, s, digestAlgorithmPrefix)
	}
	if len(hex) != digestHexLen {
		return "", fmt.Errorf("%w %q: takes %d hex digits, got %d", ErrInvalidDigestID, s, digestHexLen, len(hex))
	}
	for _, r := range hex {
		if !isHexRune(r) {
			return "", fmt.Errorf("%w %q: %q is not a hex digit", ErrInvalidDigestID, s, string(r))
		}
	}
	return ImageDigestID(s), nil
}

// String renders the id in the form the daemon takes it.
func (m ImageDigestID) String() string { return string(m) }

// UnmarshalJSON parses the id as it is read, so a record or a manifest naming
// anything but a digest fails at the decode and not at the daemon. Marshalling
// needs no counterpart: what was parsed is what is written back.
func (m *ImageDigestID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidDigestID, err)
	}
	id, err := ParseImageDigestID(s)
	if err != nil {
		return err
	}
	*m = id
	return nil
}

// isHexRune reports whether r is a lower-case hex digit — the case a digest is
// written in.
func isHexRune(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'f'
}
