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

package text

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/greenmaskio/arcave/internal/model"
)

// statusTimeLayout is how a status shows a moment, its age beside it.
const statusTimeLayout = "2006-01-02 15:04"

// formatStatus renders what the daemon runs: a title, a summary, the services, the warnings.
func (s *Printer) formatStatus(status model.ComposeProjectStatus) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render(formatStatusTitle(status)) + "\n")
	for _, line := range s.newStatusSummary(status) {
		//nolint:errcheck // a strings.Builder does not fail a write
		_, _ = fmt.Fprintf(&b, "  %-10s %s\n", line[0], line[1])
	}
	if len(status.Services) > 0 {
		b.WriteString("\n" + s.formatServiceTable(status))
	}
	b.WriteString(formatNotices(status.Notices))
	return b.String()
}

// formatStatusTitle is the compose project, its state, and the savepoint it runs.
func formatStatusTitle(status model.ComposeProjectStatus) string {
	parts := []string{status.ComposeProject.String()}
	switch status.State {
	case model.ComposeProjectStateAbsent:
		parts = append(parts, "not running")
	case model.ComposeProjectStateRollbackPending:
		parts = append(parts, "rollback in progress or interrupted")
		if inFlight := status.RollbackInFlight; inFlight != nil {
			parts = append(parts, "target "+inFlight.Target.String())
		}
	default:
		parts = append(parts, string(status.State))
	}
	deployment := status.Deployment
	switch {
	case deployment == nil || status.State == model.ComposeProjectStateRollbackPending:
	case deployment.Savepoint == nil:
		parts = append(parts, "not deployed by arcave")
	case deployment.Savepoint.SafetySnapshot:
		parts = append(parts, "safety snapshot "+deployment.Savepoint.ID.String())
	default:
		parts = append(parts, "savepoint "+formatDeployedName(*deployment.Savepoint))
	}
	return strings.Join(parts, " · ")
}

// formatDeployedName is a savepoint's name and id, its id alone when it has no name.
func formatDeployedName(savepoint model.DeployedSavepointStatus) string {
	if savepoint.Name == "" {
		return savepoint.ID.String()
	}
	return savepoint.Name + " · " + savepoint.ID.String()
}

// newStatusSummary is the summary's lines, each a key and its value.
func (s *Printer) newStatusSummary(status model.ComposeProjectStatus) [][2]string {
	var lines [][2]string
	if inFlight := status.RollbackInFlight; inFlight != nil {
		lines = append(lines, [2]string{"phase", string(inFlight.Phase) + ", since " + s.formatStatusTime(inFlight.At, status.CheckedAt)})
		if inFlight.Safety != "" {
			lines = append(lines, [2]string{"safety", inFlight.Safety.String()})
		}
	}
	if session := status.Session; session != nil {
		lines = append(lines, [2]string{"session", session.Name})
	}
	deployment := status.Deployment
	if deployment != nil && deployment.Savepoint != nil && status.State != model.ComposeProjectStateRollbackPending {
		lines = append(lines, s.newDeploymentSummary(status, *deployment)...)
	}
	return lines
}

// newDeploymentSummary is what the summary says of the deployment: when, from what session, and
// what the savepoint's record says it restored.
func (s *Printer) newDeploymentSummary(status model.ComposeProjectStatus, deployment model.DeploymentStatus) [][2]string {
	var (
		savepoint = *deployment.Savepoint
		deployed  []string
		taken     []string
	)
	if deployment.DeployedAt != nil {
		deployed = append(deployed, s.formatStatusTime(*deployment.DeployedAt, status.CheckedAt))
	}
	if replaced := countReplaced(status.Services); replaced > 0 {
		deployed = append(deployed, formatCount(replaced, "container")+" replaced since")
	}
	if savepoint.CreatedAt != nil {
		taken = append(taken, "taken "+s.formatStatusTime(*savepoint.CreatedAt, status.CheckedAt))
	}
	if restored := savepoint.Restored; restored != nil {
		taken = append(taken, "restored "+formatCount(restored.Volumes, "volume")+", "+
			formatCount(restored.WritableLayers, "layer"))
	}
	var lines [][2]string
	if len(deployed) > 0 {
		lines = append(lines, [2]string{"deployed", strings.Join(deployed, " · ")})
	}
	if deployment.Session != nil && (status.Session == nil || status.Session.ID != deployment.Session.ID) {
		lines = append(lines, [2]string{"from", "session " + deployment.Session.String()})
	}
	if len(taken) > 0 {
		lines = append(lines, [2]string{"savepoint", strings.Join(taken, " · ")})
	}
	if savepoint.Description != "" {
		lines = append(lines, [2]string{"", savepoint.Description})
	}
	return lines
}

// formatServiceTable lays the services out one per row.
func (s *Printer) formatServiceTable(status model.ComposeProjectStatus) string {
	rows := make([][]string, 0, len(status.Services))
	for _, service := range status.Services {
		replicas := strconv.Itoa(service.ReplicasUp)
		if service.ReplicasExpected > 0 {
			replicas += "/" + strconv.Itoa(service.ReplicasExpected)
		}
		rows = append(rows, []string{
			service.Name,
			replicas,
			formatServiceState(service),
			formatServiceUp(service, status.CheckedAt),
			formatServicePorts(service),
		})
	}
	return formatTable([]string{"SERVICE", "REPLICAS", "STATE", "UP", "PORTS"}, rows)
}

// formatServiceState is the service's state, its health where it runs and checks one, an exit
// code where it exited: the first failing replica's, else 0.
func formatServiceState(service model.ServiceStatus) string {
	switch {
	case service.State == model.ReplicaStateRunning && service.Health != "":
		return string(service.Health)
	case service.State == model.ReplicaStateExited:
		return "exited " + strconv.Itoa(findExitCode(service.Replicas))
	}
	return string(service.State)
}

// findExitCode is the exit code of the first replica that exited with one other than 0, else 0.
func findExitCode(replicas []model.ReplicaStatus) int {
	for _, replica := range replicas {
		if replica.State == model.ReplicaStateExited && replica.ExitCode != 0 {
			return replica.ExitCode
		}
	}
	return 0
}

// formatServiceUp is how long the service's longest-running replica has run; a dash when none runs.
func formatServiceUp(service model.ServiceStatus, checkedAt time.Time) string {
	var earliest *time.Time
	for _, replica := range service.Replicas {
		if replica.State == model.ReplicaStateRunning && replica.StartedAt != nil &&
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
func formatServicePorts(service model.ServiceStatus) string {
	type binding struct {
		hostIP        string
		hostPort      int
		containerPort int
		protocol      string
	}
	var (
		seen  = map[binding]bool{}
		ports []string
	)
	for _, replica := range service.Replicas {
		for _, port := range replica.Ports {
			key := binding{
				hostIP:        newBindingHostIP(port.HostIP),
				hostPort:      port.HostPort,
				containerPort: port.ContainerPort,
				protocol:      port.Protocol,
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			host := strconv.Itoa(port.HostPort)
			if port.HostIP != "" {
				host = port.HostIP + ":" + host
			}
			ports = append(ports, fmt.Sprintf("%s→%d/%s", host, port.ContainerPort, port.Protocol))
		}
	}
	return strings.Join(ports, ", ")
}

// newBindingHostIP is hostIP as a port's identity reads it: every unspecified address is one.
func newBindingHostIP(hostIP string) string {
	addr, err := netip.ParseAddr(hostIP)
	if err != nil || addr.IsUnspecified() {
		return ""
	}
	return addr.String()
}

// countReplaced counts the replicas the deployed savepoint recorded that something else rebuilt.
func countReplaced(services []model.ServiceStatus) int {
	replaced := 0
	for _, service := range services {
		for _, replica := range service.Replicas {
			if replica.InSavepoint && !replica.InDeployment && replica.State != model.ReplicaStateMissing {
				replaced++
			}
		}
	}
	return replaced
}

// formatStatusTime is t in the printer's zone and its age at checkedAt.
func (s *Printer) formatStatusTime(t, checkedAt time.Time) string {
	return t.In(s.opts.Location).Format(statusTimeLayout) + " (" + model.FormatAge(checkedAt.Sub(t)) + " ago)"
}
