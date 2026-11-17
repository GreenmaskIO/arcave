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
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/assert"
)

func TestNewIPAM(t *testing.T) {
	tests := []struct {
		name      string
		from      network.IPAM
		withPools bool
		want      types.IPAMConfig
	}{
		{
			name: "the driver's options are carried",
			from: network.IPAM{Driver: "default", Options: map[string]string{"foo": "bar"}},
			want: types.IPAMConfig{Driver: "default", Options: types.Options{"foo": "bar"}},
		},
		{
			name: "empty options are none",
			from: network.IPAM{Driver: "default", Options: map[string]string{}},
			want: types.IPAMConfig{Driver: "default"},
		},
		{
			name: "pools are left out unless asked for",
			from: network.IPAM{Config: []network.IPAMConfig{{Subnet: netip.MustParsePrefix("172.28.0.0/16")}}},
			want: types.IPAMConfig{},
		},
		{
			name: "every address of a pool is spelled",
			from: network.IPAM{Config: []network.IPAMConfig{{
				Subnet:     netip.MustParsePrefix("172.28.0.0/16"),
				IPRange:    netip.MustParsePrefix("172.28.5.0/24"),
				Gateway:    netip.MustParseAddr("172.28.0.1"),
				AuxAddress: map[string]netip.Addr{"host1": netip.MustParseAddr("172.28.1.5")},
			}}},
			withPools: true,
			want: types.IPAMConfig{Config: []*types.IPAMPool{{
				Subnet:             "172.28.0.0/16",
				IPRange:            "172.28.5.0/24",
				Gateway:            "172.28.0.1",
				AuxiliaryAddresses: types.Mapping{"host1": "172.28.1.5"},
			}}},
		},
		{
			name: "an unset address of a pool is none",
			from: network.IPAM{Config: []network.IPAMConfig{{
				AuxAddress: map[string]netip.Addr{"host1": {}},
			}}},
			withPools: true,
			want:      types.IPAMConfig{Config: []*types.IPAMPool{{}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newIPAM(tt.from, tt.withPools)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHasStaticAddress(t *testing.T) {
	tests := []struct {
		name     string
		endpoint *network.EndpointSettings
		want     bool
	}{
		{name: "no endpoint", endpoint: nil},
		{name: "no IPAM config", endpoint: &network.EndpointSettings{}},
		{name: "unset addresses", endpoint: &network.EndpointSettings{IPAMConfig: &network.EndpointIPAMConfig{}}},
		{
			name:     "an IPv4 address",
			endpoint: &network.EndpointSettings{IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: netip.MustParseAddr("172.28.0.10")}},
			want:     true,
		},
		{
			name:     "an IPv6 address",
			endpoint: &network.EndpointSettings{IPAMConfig: &network.EndpointIPAMConfig{IPv6Address: netip.MustParseAddr("fd00::10")}},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := hasStaticAddress(tt.endpoint)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToExposedPorts(t *testing.T) {
	tests := []struct {
		name string
		set  network.PortSet
		want types.StringOrNumberList
	}{
		{name: "none", set: nil, want: nil},
		{
			name: "each key spelled port/protocol, sorted",
			set: network.PortSet{
				network.MustParsePort("80/tcp"):   {},
				network.MustParsePort("53/udp"):   {},
				network.MustParsePort("132/sctp"): {},
			},
			want: types.StringOrNumberList{"132/sctp", "53/udp", "80/tcp"},
		},
		{
			name: "a range is reported one port at a time",
			set: network.PortSet{
				network.MustParsePort("8000/tcp"): {},
				network.MustParsePort("8001/tcp"): {},
			},
			want: types.StringOrNumberList{"8000/tcp", "8001/tcp"},
		},
		{
			name: "a zero port is left out",
			set:  network.PortSet{{}: {}, network.MustParsePort("80/tcp"): {}},
			want: types.StringOrNumberList{"80/tcp"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := toExposedPorts(tt.set)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewPublishedPorts(t *testing.T) {
	tests := []struct {
		name     string
		bindings network.PortMap
		want     []types.ServicePortConfig
	}{
		{name: "none", bindings: nil, want: nil},
		{
			name: "a binding on a host address",
			bindings: network.PortMap{
				network.MustParsePort("5432/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "15432"}},
			},
			want: []types.ServicePortConfig{{Target: 5432, Protocol: "tcp", Published: "15432", HostIP: "127.0.0.1"}},
		},
		{
			name: "a binding on no host address names none",
			bindings: network.PortMap{
				network.MustParsePort("53/udp"): {{HostPort: "1053"}},
			},
			want: []types.ServicePortConfig{{Target: 53, Protocol: "udp", Published: "1053"}},
		},
		{
			name: "a published range is one entry per port",
			bindings: network.PortMap{
				network.MustParsePort("8001/tcp"): {{HostPort: "9001"}},
				network.MustParsePort("8000/tcp"): {{HostPort: "9000"}},
			},
			want: []types.ServicePortConfig{
				{Target: 8000, Protocol: "tcp", Published: "9000"},
				{Target: 8001, Protocol: "tcp", Published: "9001"},
			},
		},
		{
			name:     "a port bound nowhere is none",
			bindings: network.PortMap{network.MustParsePort("80/tcp"): nil},
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newPublishedPorts(tt.bindings)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToAddrStrings(t *testing.T) {
	tests := []struct {
		name  string
		addrs []netip.Addr
		want  []string
	}{
		{name: "none", addrs: nil, want: nil},
		{name: "an unset address is left out", addrs: []netip.Addr{{}}, want: nil},
		{
			name:  "each set address is spelled",
			addrs: []netip.Addr{netip.MustParseAddr("8.8.8.8"), {}, netip.MustParseAddr("fe80::1")},
			want:  []string{"8.8.8.8", "fe80::1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := toAddrStrings(tt.addrs)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
