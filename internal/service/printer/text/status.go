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

// formatStatus renders what runs: a title, a summary, the provider's table, the warnings.
func (s *Printer) formatStatus(status model.ComposeProjectStatus) string {
	var (
		b     strings.Builder
		table = newStatusTable(status)
	)
	b.WriteString(styleTitle.Render(formatStatusTitle(status, len(table.Rows) > 0)) + "\n")
	for _, line := range s.newStatusSummary(status) {
		fmt.Fprintf(&b, "  %-10s %s\n", line.key, line.value)
	}
	if len(table.Rows) > 0 {
		b.WriteString("\n" + formatTable(table.Columns, table.Rows))
	}
	b.WriteString(formatNotices(status.Notices))
	return b.String()
}

// newStatusTable is the provider's table of the status; none without a detail.
func newStatusTable(status model.ComposeProjectStatus) model.Table {
	if status.Detail == nil {
		return model.Table{}
	}
	return status.Detail.Table()
}

// formatStatusTitle is the compose project, its state, and what it runs; members says the table
// lists any.
func formatStatusTitle(status model.ComposeProjectStatus, members bool) string {
	parts := []string{status.ComposeProject.String()}
	if status.State == model.StatusStateRollbackPending {
		parts = append(parts, "rollback in progress or interrupted")
		if inFlight := status.RollbackInFlight; inFlight != nil {
			parts = append(parts, "target "+inFlight.Target.String())
		}
		return strings.Join(parts, " · ")
	}
	parts = append(parts, status.State)
	switch deployment := status.Deployment; {
	case deployment != nil && deployment.Savepoint != nil:
		parts = append(parts, formatDeployedSavepoint(*deployment.Savepoint))
	case deployment == nil && members:
		parts = append(parts, "not deployed by arcave")
	}
	return strings.Join(parts, " · ")
}

// newShownDeployment is the deployment a status shows; nil when nothing is deployed, or while a
// rollback is pending, which is replacing it.
func newShownDeployment(status model.ComposeProjectStatus) *model.DeploymentStatus {
	if status.State == model.StatusStateRollbackPending {
		return nil
	}
	return status.Deployment
}

// formatDeployedSavepoint names what the workload runs: the savepoint as the notices name it, a
// safety snapshot by its id alone, since its name is no selector and is empty.
func formatDeployedSavepoint(savepoint model.DeployedSavepointStatus) string {
	if savepoint.IsSafetySnapshot {
		return "safety snapshot " + savepoint.ID.String()
	}
	return "savepoint " + savepoint.String()
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
			value: session.String(),
		})
	}
	if deployment := newShownDeployment(status); deployment != nil && deployment.Savepoint != nil {
		lines = append(lines, s.newDeploymentSummary(status.CheckedAt, status.Session, *deployment)...)
	}
	return lines
}

// newDeploymentSummary is what the summary says of the deployment: when, from what session, and
// when its savepoint was taken.
func (s *Printer) newDeploymentSummary(
	checkedAt time.Time, session *model.SessionRef, deployment model.DeploymentStatus,
) []summaryLine {
	var (
		savepoint = *deployment.Savepoint
		lines     []summaryLine
	)
	lines = append(lines, summaryLine{
		key:   "deployed",
		value: s.formatStatusTime(deployment.DeployedAt, checkedAt),
	})
	if session == nil || session.ID != deployment.Session.ID {
		lines = append(lines, summaryLine{
			key:   "from",
			value: "session " + deployment.Session.String(),
		})
	}
	lines = append(lines, summaryLine{
		key:   "savepoint",
		value: "taken " + s.formatStatusTime(savepoint.CreatedAt, checkedAt),
	})
	if savepoint.Description != "" {
		lines = append(lines, summaryLine{value: savepoint.Description})
	}
	return lines
}

// formatStatusTime is t in the printer's zone and its age at checkedAt.
func (s *Printer) formatStatusTime(t, checkedAt time.Time) string {
	return s.formatTime(t) + " (" + model.FormatAge(checkedAt.Sub(t)) + " ago)"
}
