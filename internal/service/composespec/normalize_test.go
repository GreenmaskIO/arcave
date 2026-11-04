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
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
)

func TestToSignalName(t *testing.T) {
	tests := []struct {
		name   string
		signal string
		want   string
	}{
		{name: "a name, as Docker reports it", signal: "SIGINT", want: "SIGINT"},
		{name: "a number Linux names", signal: "15", want: "SIGTERM"},
		{name: "a number past the common few", signal: "13", want: "SIGPIPE"},
		{name: "a number Linux does not name", signal: "64", want: "64"},
		{name: "none", signal: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := toSignalName(tt.signal)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToUlimitNames(t *testing.T) {
	limit := &types.UlimitsConfig{Soft: 1024, Hard: 2048}
	tests := []struct {
		name    string
		ulimits map[string]*types.UlimitsConfig
		want    map[string]*types.UlimitsConfig
	}{
		{name: "compose's names, as Docker reports them", ulimits: map[string]*types.UlimitsConfig{"nofile": limit},
			want: map[string]*types.UlimitsConfig{"nofile": limit}},
		{name: "the kernel's names", ulimits: map[string]*types.UlimitsConfig{"RLIMIT_NOFILE": limit},
			want: map[string]*types.UlimitsConfig{"nofile": limit}},
		{name: "none", ulimits: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := toUlimitNames(tt.ulimits)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToCapabilityNames(t *testing.T) {
	tests := []struct {
		name         string
		capabilities []string
		want         []string
	}{
		{name: "the kernel's spelling, as Docker reports it", capabilities: []string{"CAP_NET_ADMIN", "CAP_MKNOD"},
			want: []string{"NET_ADMIN", "MKNOD"}},
		{name: "compose's spelling", capabilities: []string{"NET_ADMIN", "ALL"}, want: []string{"NET_ADMIN", "ALL"}},
		{name: "none", capabilities: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := toCapabilityNames(tt.capabilities)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToSecurityOptNames(t *testing.T) {
	tests := []struct {
		name    string
		options []string
		want    []string
	}{
		{name: "with its value, as Docker reports it", options: []string{"no-new-privileges:true"},
			want: []string{"no-new-privileges:true"}},
		{name: "bare", options: []string{"no-new-privileges", "seccomp=unconfined"},
			want: []string{"no-new-privileges:true", "seccomp=unconfined"}},
		{name: "none", options: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := toSecurityOptNames(tt.options)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDropCPUQuotaOfNanoCPUs(t *testing.T) {
	tests := []struct {
		name      string
		resources container.Resources
		want      container.Resources
	}{
		{name: "cpus alone, as Docker reports it", resources: container.Resources{NanoCPUs: 5e8},
			want: container.Resources{NanoCPUs: 5e8}},
		{name: "the quota spelling cpus", resources: container.Resources{NanoCPUs: 5e8, CPUQuota: 50000, CPUPeriod: 100000},
			want: container.Resources{NanoCPUs: 5e8}},
		{name: "a quota of its own", resources: container.Resources{NanoCPUs: 5e8, CPUQuota: 20000, CPUPeriod: 100000},
			want: container.Resources{NanoCPUs: 5e8, CPUQuota: 20000, CPUPeriod: 100000}},
		{name: "a quota with no cpus", resources: container.Resources{CPUQuota: 50000, CPUPeriod: 100000},
			want: container.Resources{CPUQuota: 50000, CPUPeriod: 100000}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := dropCPUQuotaOfNanoCPUs(tt.resources)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDropDerivedDriverOpts(t *testing.T) {
	tests := []struct {
		name    string
		options map[string]string
		want    map[string]string
	}{
		{name: "the declaration alone, as Docker reports it", options: map[string]string{"o": "size=10m", "type": "tmpfs"},
			want: map[string]string{"o": "size=10m", "type": "tmpfs"}},
		{name: "derived from o", options: map[string]string{"o": "uid=1,size=10m", "SIZE": "10m"},
			want: map[string]string{"o": "uid=1,size=10m"}},
		{name: "not what o says", options: map[string]string{"o": "size=10m", "SIZE": "1g"},
			want: map[string]string{"o": "size=10m", "SIZE": "1g"}},
		{name: "none", options: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := dropDerivedDriverOpts(tt.options)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDropLatestOfBuiltImage(t *testing.T) {
	built := image.InspectResponse{Config: &dockerspec.DockerOCIImageConfig{
		ImageConfig: ocispec.ImageConfig{Labels: map[string]string{api.ProjectLabel: "demo"}},
	}}
	tests := []struct {
		name  string
		ref   string
		image image.InspectResponse
		want  string
	}{
		{name: "a bare name, as Docker reports it", ref: "demo-app", image: built, want: "demo-app"},
		{name: "a built image tagged latest", ref: "demo-app:latest", image: built, want: "demo-app"},
		{name: "a pulled image tagged latest", ref: "busybox:latest", image: image.InspectResponse{}, want: "busybox:latest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := dropLatestOfBuiltImage(tt.ref, tt.image)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
