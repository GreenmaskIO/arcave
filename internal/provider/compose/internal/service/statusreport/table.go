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
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

// tableColumns name the columns of a status's service table.
var tableColumns = []string{"SERVICE", "REPLICAS", "STATE", "UP", "PORTS"}

// NewTable lays detail's services out one per row, every age counted from checkedAt; no rows for
// none.
func NewTable(detail composemodel.StatusDetail, checkedAt time.Time) model.Table {
	var rows [][]string
	for _, service := range detail.Services {
		replicas := strconv.Itoa(service.ReplicasUp)
		if service.ReplicasExpected > 0 {
			replicas += "/" + strconv.Itoa(service.ReplicasExpected)
		}
		rows = append(rows, []string{
			service.Name,
			replicas,
			formatServiceState(service),
			formatServiceUp(service, checkedAt),
			formatServicePorts(service),
		})
	}
	return model.Table{
		Columns: tableColumns,
		Rows:    rows,
	}
}

// formatServiceState is the service's state, its health where it runs and checks one, an exit
// code where it exited: the first failing replica's, else 0.
func formatServiceState(service composemodel.ServiceStatus) string {
	switch {
	case service.State == composemodel.ReplicaStateRunning && service.Health != "":
		return string(service.Health)
	case service.State == composemodel.ReplicaStateExited:
		return "exited " + strconv.Itoa(findExitCode(service.Replicas))
	}
	return string(service.State)
}

// findExitCode is the exit code of the first replica that exited with one other than 0, else 0.
func findExitCode(replicas []composemodel.ReplicaStatus) int {
	for _, replica := range replicas {
		if replica.State == composemodel.ReplicaStateExited && replica.ExitCode != 0 {
			return replica.ExitCode
		}
	}
	return 0
}

// formatServiceUp is how long the service's longest-running replica has run; a dash when none runs.
func formatServiceUp(service composemodel.ServiceStatus, checkedAt time.Time) string {
	var earliest *time.Time
	for _, replica := range service.Replicas {
		if replica.State == composemodel.ReplicaStateRunning && replica.StartedAt != nil &&
			(earliest == nil || replica.StartedAt.Before(*earliest)) {
			earliest = replica.StartedAt
		}
	}
	if earliest == nil {
		return "—"
	}
	return model.FormatAge(checkedAt.Sub(*earliest))
}

// formatServicePorts lists the ports the service publishes once each: one bound on both 0.0.0.0
// and :: is one port to a reader, one bound on two given addresses two.
func formatServicePorts(service composemodel.ServiceStatus) string {
	var (
		seen  = map[composemodel.PublishedPort]struct{}{}
		ports []string
	)
	for _, replica := range service.Replicas {
		for _, port := range replica.Ports {
			key := port
			key.HostIP = toBindingHostIP(port.HostIP)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			host := strconv.Itoa(port.HostPort)
			if port.HostIP != "" {
				host = port.HostIP + ":" + host
			}
			ports = append(ports, fmt.Sprintf("%s→%d/%s", host, port.ContainerPort, port.Protocol))
		}
	}
	return strings.Join(ports, ", ")
}

// toBindingHostIP is hostIP as a port's identity reads it: every unspecified address is one.
func toBindingHostIP(hostIP string) string {
	addr, err := netip.ParseAddr(hostIP)
	if err != nil || addr.IsUnspecified() {
		return ""
	}
	return addr.String()
}
