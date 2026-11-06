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
			created = session.CreatedAt.In(s.opts.Location).Format("2006-01-02 15:04")
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
			strconv.Itoa(session.SavepointCount),
			formatSize(session.SizeBytes),
		})
	}
	return formatTable([]string{"  NAME", "ID", "STATUS", "CREATED", "SAVEPOINTS", "SIZE"}, rows)
}

// formatRemoveSession is the session a remove took and what it freed.
func formatRemoveSession(response model.RemoveSessionResponse) string {
	if !response.Removed {
		return fmt.Sprintf("Session %s was not removed.\n", response.Session)
	}
	return fmt.Sprintf("Removed session %s · freed %s\n", response.Session, formatSize(response.FreedBytes))
}
