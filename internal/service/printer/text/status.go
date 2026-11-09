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

// summaryLine is one line of a status's summary: a key and its value, the key blank for a line
// that goes on the one above.
type summaryLine struct {
	key   string
	value string
}

// formatStatus renders what the daemon runs: a title, a summary, the services, the warnings.
func (s *Printer) formatStatus(status model.ComposeProjectStatus) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render(formatStatusTitle(status)) + "\n")
	for _, line := range s.newStatusSummary(status) {
		fmt.Fprintf(&b, "  %-10s %s\n", line.key, line.value)
	}
	if len(status.Services) > 0 {
		b.WriteString("\n" + formatServiceTable(status.Services, status.CheckedAt))
	}
	b.WriteString(formatNotices(status.Notices))
	return b.String()
}

// formatStatusTitle is the compose project, its state, and what it runs.
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
	if deployment := newShownDeployment(status); deployment != nil {
		parts = append(parts, formatDeploymentSource(*deployment))
	}
	return strings.Join(parts, " · ")
}

// newShownDeployment is the deployment a status shows; nil when nothing runs, or while a rollback
// is pending, which is replacing it.
func newShownDeployment(status model.ComposeProjectStatus) *model.DeploymentStatus {
	if status.State == model.ComposeProjectStateRollbackPending {
		return nil
	}
	return status.Deployment
}

// formatDeploymentSource names what the containers run: the savepoint as the notices name it, a
// safety snapshot by its id alone, since its name is the id of the savepoint it was taken before.
func formatDeploymentSource(deployment model.DeploymentStatus) string {
	switch savepoint := deployment.Savepoint; {
	case savepoint == nil:
		return "not deployed by arcave"
	case savepoint.IsSafetySnapshot:
		return "safety snapshot " + savepoint.ID.String()
	default:
		return "savepoint " + savepoint.String()
	}
}

// newStatusSummary is the summary's lines: the rollback in flight, the session, the deployment.
func (s *Printer) newStatusSummary(status model.ComposeProjectStatus) []summaryLine {
	var lines []summaryLine
	if inFlight := status.RollbackInFlight; inFlight != nil {
		lines = append(lines, summaryLine{
			key:   "phase",
			value: string(inFlight.Phase) + ", since " + s.formatStatusTime(inFlight.At, status.CheckedAt),
		})
		if inFlight.Safety != "" {
			lines = append(lines, summaryLine{
				key:   "safety",
				value: inFlight.Safety.String(),
			})
		}
	}
	if session := status.Session; session != nil {
		lines = append(lines, summaryLine{
			key:   "session",
			value: session.Name,
		})
	}
	if deployment := newShownDeployment(status); deployment != nil && deployment.Savepoint != nil {
		lines = append(lines, s.newDeploymentSummary(status.CheckedAt, status.Session, *deployment)...)
	}
	return lines
}

// newDeploymentSummary is what the summary says of the deployment: when, from what session, and
// what the savepoint's record says it restored.
func (s *Printer) newDeploymentSummary(
	checkedAt time.Time, session *model.SessionRef, deployment model.DeploymentStatus,
) []summaryLine {
	var (
		savepoint = *deployment.Savepoint
		deployed  = []string{s.formatStatusTime(deployment.DeployedAt, checkedAt)}
		taken     = []string{"taken " + s.formatStatusTime(savepoint.CreatedAt, checkedAt)}
		lines     []summaryLine
	)
	if deployment.Replaced > 0 {
		deployed = append(deployed, formatCount(deployment.Replaced, "container")+" replaced since")
	}
	if restored := savepoint.Restored; restored != nil {
		taken = append(taken, "restored "+formatCount(restored.Volumes, "volume")+", "+
			formatCount(restored.WritableLayers, "layer"))
	}
	lines = append(lines, summaryLine{
		key:   "deployed",
		value: strings.Join(deployed, " · "),
	})
	if session == nil || session.ID != deployment.Session.ID {
		lines = append(lines, summaryLine{
			key:   "from",
			value: "session " + deployment.Session.String(),
		})
	}
	lines = append(lines, summaryLine{
		key:   "savepoint",
		value: strings.Join(taken, " · "),
	})
	if savepoint.Description != "" {
		lines = append(lines, summaryLine{value: savepoint.Description})
	}
	return lines
}

// formatServiceTable lays the services out one per row.
func formatServiceTable(services []model.ServiceStatus, checkedAt time.Time) string {
	rows := make([][]string, 0, len(services))
	for _, service := range services {
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
	var (
		seen  = map[model.PublishedPort]struct{}{}
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

// formatStatusTime is t in the printer's zone and its age at checkedAt.
func (s *Printer) formatStatusTime(t, checkedAt time.Time) string {
	return s.formatTime(t) + " (" + model.FormatAge(checkedAt.Sub(t)) + " ago)"
}
