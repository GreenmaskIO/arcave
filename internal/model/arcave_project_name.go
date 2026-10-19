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
	"fmt"
	"strings"
)

// ArcaveProjectName names the arcave project every store root is scoped by — the
// `project.name` of .arcave/config.yaml. It carries its own gate, so a value
// that reached a store was validated.
//
// Deliberately not DockerComposeProjectName: the arcave project and the compose project are
// different concepts, and one segment must never be passed where the other is expected.
type ArcaveProjectName string

// Validate accepts a non-empty name of the charset compose imposes on project
// names: it starts with [a-z0-9] and continues with [a-z0-9_-].
//
// An allowlist, like SessionID's and SavepointID's, and for the same reason: the
// name is rendered into a store root, and a list of what a path segment must not
// be is never provably complete.
func (m ArcaveProjectName) Validate() error {
	if err := validateNameSegment(string(m)); err != nil {
		return fmt.Errorf("invalid arcave project name %q: %w", string(m), err)
	}
	return nil
}

// String renders the name as the path segment it becomes.
func (m ArcaveProjectName) String() string { return string(m) }

// NormalizeArcaveProjectName coerces s into the charset Validate accepts — the same
// normalization compose-go applies to a directory name: lower case, characters
// outside the charset dropped, leading "-"/"_" trimmed. It is spelled out here
// rather than borrowed, since model imports nothing.
//
// The result may be empty (a name of nothing but non-ASCII letters), which
// Validate is what reports.
func NormalizeArcaveProjectName(s string) ArcaveProjectName {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if isNameRune(r) {
			b.WriteRune(r)
		}
	}
	return ArcaveProjectName(strings.TrimLeft(b.String(), "-_"))
}

// validateNameSegment holds ArcaveProjectName's charset rule.
// The message names the condition alone; each type wraps it with its own
// subject.
func validateNameSegment(s string) error {
	if s == "" {
		return fmt.Errorf("must not be empty")
	}
	for i, r := range s {
		if !isNameRune(r) || (i == 0 && (r == '-' || r == '_')) {
			return fmt.Errorf("must start with [a-z0-9] and continue with [a-z0-9_-]")
		}
	}
	return nil
}

// isNameRune reports whether r is in the [a-z0-9_-] charset.
func isNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-'
}
