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

import "fmt"

// SortOrder orders a listing by age.
type SortOrder string

const (
	// SortDesc lists the newest first; the zero value means it too.
	SortDesc SortOrder = "desc"
	// SortAsc lists the oldest first.
	SortAsc SortOrder = "asc"
)

// ParseSortOrder validates a sort token; an empty one is SortDesc.
func ParseSortOrder(token string) (SortOrder, error) {
	switch order := SortOrder(token); order {
	case "", SortDesc:
		return SortDesc, nil
	case SortAsc:
		return order, nil
	default:
		return "", fmt.Errorf("%w: %q, want %q or %q", ErrInvalidSortOrder, token, SortDesc, SortAsc)
	}
}

// ListRequest selects which savepoints a listing answers with. A struct rather
// than a bool parameter, like every request type here: the next flag does not
// change the signature.
type ListRequest struct {
	// IncludeSafetySnapshots adds the undo records a rollback commits. Hidden by
	// default: one exists only while a rollback runs, and a leftover is the shard
	// of a killed one.
	IncludeSafetySnapshots bool
	// Sort orders the listing by age; zero is newest first.
	Sort SortOrder
}
