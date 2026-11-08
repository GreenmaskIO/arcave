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

package composespec

import (
	"net/netip"

	"github.com/compose-spec/compose-go/v2/types"
)

// netValue is an address or a prefix: an unset one spells itself "invalid …".
type netValue interface {
	IsValid() bool
	String() string
}

// toNetString spells value as compose does, an unset one as none.
func toNetString[T netValue](value T) string {
	if !value.IsValid() {
		return ""
	}
	return value.String()
}

// toAddrStrings spells each set address, nil when none is.
func toAddrStrings(addrs []netip.Addr) []string {
	var spelled []string
	for _, addr := range addrs {
		if addr.IsValid() {
			spelled = append(spelled, addr.String())
		}
	}
	return spelled
}

// toAddrMapping spells each set address under its name, nil when none is.
func toAddrMapping(addrs map[string]netip.Addr) types.Mapping {
	var spelled types.Mapping
	for name, addr := range addrs {
		if !addr.IsValid() {
			continue
		}
		if spelled == nil {
			spelled = types.Mapping{}
		}
		spelled[name] = addr.String()
	}
	return spelled
}
