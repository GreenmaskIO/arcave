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
	"strconv"
	"strings"

	"github.com/greenmaskio/arcave/internal/model"
)

// formatSession is one session: its name, its id and where it was cloned from.
func formatSession(session model.Session) string {
	line := fmt.Sprintf("Session %s · %s", session.Name, session.ID)
	if session.From != nil {
		line += " · from " + session.From.Session.String()
	}
	return line + "\n"
}

// formatSessions is a session listing as a table, the current one starred.
func (s *Printer) formatSessions(sessions []model.SessionSummary) string {
	if len(sessions) == 0 {
		return "No sessions.\n"
	}
	rows := make([][]string, 0, len(sessions))
	for _, session := range sessions {
		marker := " "
		if session.Current {
			marker = "*"
		}
		created := "-"
		if !session.CreatedAt.IsZero() {
			created = s.formatTime(session.CreatedAt)
		}
		name := session.Name
		if name == "" {
			name = "-"
		}
		rows = append(rows, []string{
			marker + " " + name,
			session.ID.String(),
			string(session.Status),
			created,
			formatSavepointCount(session.SavepointCount, session.SafetySnapshotCount),
			formatSize(session.SizeBytes),
			formatComposeProjectNames(session.ComposeProjects),
		})
	}
	return formatTable([]string{"  NAME", "ID", "STATUS", "CREATED", "SAVEPOINTS", "SIZE", "COMPOSE PROJECTS"}, rows)
}

// formatSavepointCount is the savepoints counted, the safety snapshots added when any.
func formatSavepointCount(savepoints, safetySnapshots int) string {
	if safetySnapshots == 0 {
		return strconv.Itoa(savepoints)
	}
	return fmt.Sprintf("%d (+%d safety)", savepoints, safetySnapshots)
}

// formatComposeProjectNames names up to two compose projects and counts the rest.
func formatComposeProjectNames(names []model.DockerComposeProjectName) string {
	const shown = 2
	if len(names) == 0 {
		return "-"
	}
	parts := make([]string, 0, shown)
	for _, name := range names[:min(len(names), shown)] {
		parts = append(parts, name.String())
	}
	line := strings.Join(parts, ", ")
	if len(names) > shown {
		line += fmt.Sprintf(" +%d", len(names)-shown)
	}
	return line
}

// formatRemoveSession is the session a remove took and what it freed.
func formatRemoveSession(response model.RemoveSessionResponse) string {
	if !response.Removed {
		return fmt.Sprintf("Session %s was not removed.\n", response.Session)
	}
	return fmt.Sprintf("Removed session %s · freed %s\n", response.Session, formatSize(response.FreedBytes))
}

// formatSessionDetail is one session: a title, then each compose project's savepoints as a table.
func (s *Printer) formatSessionDetail(detail model.SessionDetail) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render(formatSessionDetailTitle(detail)) + "\n")
	if !detail.Session.CreatedAt.IsZero() {
		fmt.Fprintf(&b, "  %-10s %s (%s ago)\n", "Created", s.formatTime(detail.Session.CreatedAt),
			model.FormatAge(detail.CheckedAt.Sub(detail.Session.CreatedAt)))
	}
	if detail.From != nil {
		fmt.Fprintf(&b, "  %-10s %s\n", "From", detail.From.ID)
		if detail.From.Name != "" {
			fmt.Fprintf(&b, "  %-10s %s\n", "", detail.From.Name)
		}
	}
	fmt.Fprintf(&b, "  %-10s %s\n", "Savepoints",
		formatSavepointCount(detail.SavepointCount, detail.SafetySnapshotCount))
	switch {
	case len(detail.ComposeProjects) == 0 && detail.SafetySnapshotCount > 0:
		b.WriteString("\nNo savepoints shown: safety snapshots are hidden; --include-safety-snapshots shows them.\n")
		return b.String()
	case len(detail.ComposeProjects) == 0:
		b.WriteString("\nNo savepoints in this session yet.\n")
		return b.String()
	}
	for _, group := range detail.ComposeProjects {
		b.WriteString("\n" + styleTitle.Render(group.ComposeProject.String()) + "\n")
		b.WriteString(indentLines(s.formatSavepointTable(group.Savepoints), "  "))
	}
	return b.String()
}

// formatSessionDetailTitle is the session's name, its id, and whether it is the current one.
func formatSessionDetailTitle(detail model.SessionDetail) string {
	parts := []string{"Session"}
	if detail.Session.Name != "" {
		parts = append(parts, detail.Session.Name+" ·")
	}
	parts = append(parts, detail.Session.ID.String())
	if detail.Current {
		parts = append(parts, "· current")
	}
	return strings.Join(parts, " ")
}

// indentLines prefixes every line of text with indent.
func indentLines(text, indent string) string {
	lines := strings.SplitAfter(text, "\n")
	var b strings.Builder
	for _, line := range lines {
		if line != "" {
			b.WriteString(indent + line)
		}
	}
	return b.String()
}
