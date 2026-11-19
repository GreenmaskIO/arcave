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

import "slices"

// UnitKind is a kind of unit a summary totals, as its rows and columns name it.
type UnitKind string

const (
	UnitKindVolumes UnitKind = "volumes"
	UnitKindLayers  UnitKind = "layers"
	UnitKindImages  UnitKind = "images"
)

// ReplicaHeldLayer is what a replica holding marks a writable layer by.
const ReplicaHeldLayer = "layer"

// SavepointSummary is what a provider sums a savepoint up as, for arcave to print and judge by
// without reading the body.
type SavepointSummary struct {
	// Units total what the savepoint holds by kind, in the order to print them.
	Units []UnitTotal `json:"units"`
	// Notices say what the capture left out, the severe first.
	Notices []Notice `json:"notices,omitempty"`
	// Gaps say what a rollback to the savepoint does not put back, as a status of its deployment
	// reports it.
	Gaps []Notice `json:"gaps,omitempty"`
	// Replicas name what the savepoint holds of each replica beside the volumes.
	Replicas []ReplicaHolding `json:"replicas,omitempty"`
}

// UnitTotal is one kind of unit a savepoint holds: how many, their size and what storing them took.
type UnitTotal struct {
	Kind        UnitKind `json:"kind"`
	Count       int      `json:"count"`
	SizeBytes   int64    `json:"size_bytes"`
	StoredBytes int64    `json:"stored_bytes"`
}

// ReplicaHolding is what a savepoint holds of one replica: ReplicaHeldLayer for its writable layer.
type ReplicaHolding struct {
	Service string   `json:"service"`
	Replica int      `json:"replica"`
	Held    []string `json:"held"`
}

// Unit is the total of kind; its zero total when the savepoint holds none.
func (m SavepointSummary) Unit(kind UnitKind) UnitTotal {
	for _, unit := range m.Units {
		if unit.Kind == kind {
			return unit
		}
	}
	return UnitTotal{Kind: kind}
}

// StoredBytes is what the savepoint wrote to the store, every kind of unit together.
func (m SavepointSummary) StoredBytes() int64 {
	var stored int64
	for _, unit := range m.Units {
		stored += unit.StoredBytes
	}
	return stored
}

// Holds reports whether the savepoint holds what of the replica of service numbered replica.
func (m SavepointSummary) Holds(service string, replica int, what string) bool {
	for _, holding := range m.Replicas {
		if holding.Service == service && holding.Replica == replica && slices.Contains(holding.Held, what) {
			return true
		}
	}
	return false
}
