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

package statusreport

import (
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

func TestNewTable(t *testing.T) {
	started := statusCheckedAt.Add(-(3*time.Hour + 11*time.Minute))
	tests := []struct {
		name   string
		detail composemodel.StatusDetail
		want   model.Table
	}{
		{name: "no service is no row", want: model.Table{Columns: tableColumns}},
		{
			name: "one row per service, the expected replicas where a savepoint recorded them",
			detail: composemodel.StatusDetail{Services: []composemodel.ServiceStatus{
				{
					Name: "db", State: composemodel.ReplicaStateRunning, Health: composemodel.ReplicaHealthHealthy,
					ReplicasExpected: 1, ReplicasUp: 1,
					Replicas: []composemodel.ReplicaStatus{{
						Number: 1, State: composemodel.ReplicaStateRunning, StartedAt: &started,
						Ports: []composemodel.PublishedPort{publishedPort("127.0.0.1", 5432, 5432)},
					}},
				},
				{
					Name: "migrate", State: composemodel.ReplicaStateCompleted, ReplicasUp: 1,
					Replicas: []composemodel.ReplicaStatus{{Number: 1, State: composemodel.ReplicaStateCompleted}},
				},
			}},
			want: model.Table{Columns: tableColumns, Rows: [][]string{
				{"db", "1/1", "healthy", "3h 11m", "127.0.0.1:5432→5432/tcp"},
				{"migrate", "1", "completed", "—", ""},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := NewTable(tt.detail, statusCheckedAt)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatServicePorts(t *testing.T) {
	tests := []struct {
		name  string
		ports []composemodel.PublishedPort
		want  string
	}{
		{name: "none"},
		{
			name:  "one port on both unspecified addresses is one",
			ports: []composemodel.PublishedPort{publishedPort("0.0.0.0", 8080, 80), publishedPort("::", 8080, 80)},
			want:  "0.0.0.0:8080→80/tcp",
		},
		{
			name:  "one port on two given addresses is two",
			ports: []composemodel.PublishedPort{publishedPort("127.0.0.1", 5432, 5432), publishedPort("192.168.1.5", 5432, 5432)},
			want:  "127.0.0.1:5432→5432/tcp, 192.168.1.5:5432→5432/tcp",
		},
		{
			name:  "a port with no address",
			ports: []composemodel.PublishedPort{{ContainerPort: 80, Protocol: "udp", HostPort: 8080}},
			want:  "8080→80/udp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			service := composemodel.ServiceStatus{Replicas: []composemodel.ReplicaStatus{{
				Number: 1, State: composemodel.ReplicaStateRunning, Ports: tt.ports,
			}}}

			// Act
			got := formatServicePorts(service)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatServiceState(t *testing.T) {
	tests := []struct {
		name    string
		service composemodel.ServiceStatus
		want    string
	}{
		{
			name: "every replica exited 0",
			service: composemodel.ServiceStatus{State: composemodel.ReplicaStateExited, Replicas: []composemodel.ReplicaStatus{
				{Number: 1, State: composemodel.ReplicaStateExited},
				{Number: 2, State: composemodel.ReplicaStateExited},
			}},
			want: "exited 0",
		},
		{
			name: "a failing replica after one that exited 0",
			service: composemodel.ServiceStatus{State: composemodel.ReplicaStateExited, Replicas: []composemodel.ReplicaStatus{
				{Number: 1, State: composemodel.ReplicaStateExited},
				{Number: 2, State: composemodel.ReplicaStateExited, ExitCode: 137},
			}},
			want: "exited 137",
		},
		{
			name:    "running with a healthcheck",
			service: composemodel.ServiceStatus{State: composemodel.ReplicaStateRunning, Health: composemodel.ReplicaHealthHealthy},
			want:    "healthy",
		},
		{
			name:    "running with none",
			service: composemodel.ServiceStatus{State: composemodel.ReplicaStateRunning},
			want:    "running",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := formatServiceState(tt.service)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func publishedPort(hostIP string, hostPort, containerPort int) composemodel.PublishedPort {
	return composemodel.PublishedPort{
		ContainerPort: containerPort,
		Protocol:      "tcp",
		HostIP:        netip.MustParseAddr(hostIP).String(),
		HostPort:      hostPort,
	}
}
