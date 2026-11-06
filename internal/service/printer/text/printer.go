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

// Package text renders a command's result for a person: a summary, a table, the next command.
package text

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/greenmaskio/arcave/internal/model"
	jsonprinter "github.com/greenmaskio/arcave/internal/service/printer/json"
)

var (
	styleTitle         = lipgloss.NewStyle().Bold(true)
	styleWarningsTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Yellow)
	styleWarning       = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	styleHint          = lipgloss.NewStyle().Faint(true)
)

// Options are what the next commands a result suggests are spelled with, and how it is colored.
type Options struct {
	// ComposeProject is -p as the run named it; empty for a command bound to none.
	ComposeProject string
	// Session is --session as the run named it; empty for the current session.
	Session string
	// Location is the zone times are shown in; nil is the local one.
	Location *time.Location
	// Profile is the color profile of the terminal the output ends in; the zero value writes no color.
	Profile colorprofile.Profile
}

// Printer renders a result as text; a type it does not know goes out as JSON.
type Printer struct {
	w    io.Writer
	opts Options
}

// New returns a Printer writing to w.
func New(w io.Writer, opts Options) *Printer {
	if opts.Location == nil {
		opts.Location = time.Local
	}
	return &Printer{
		w: &colorprofile.Writer{
			Forward: w,
			Profile: opts.Profile,
		},
		opts: opts,
	}
}

// Print writes v as text.
func (s *Printer) Print(v any) error {
	var text string
	switch v := v.(type) {
	case model.Savepoint:
		text = s.formatSavepoint(v)
	case []model.Savepoint:
		text = s.formatSavepoints(v)
	case model.RollbackResponse:
		text = s.formatRollback(v)
	case model.RemoveResponse:
		text = s.formatRemove(v)
	case model.GarbageCollectResult:
		text = s.formatGarbageCollect(v)
	case model.Session:
		text = formatSession(v)
	case []model.SessionSummary:
		text = s.formatSessions(v)
	case model.RemoveSessionResponse:
		text = formatRemoveSession(v)
	default:
		return jsonprinter.New(s.w).Print(v)
	}
	_, err := io.WriteString(s.w, text)
	return err
}

// newCommand spells an arcave command with the -p and --session the run named.
func (s *Printer) newCommand(composeBound bool, args ...string) string {
	parts := []string{"arcave"}
	if composeBound && s.opts.ComposeProject != "" {
		parts = append(parts, "-p", quote(s.opts.ComposeProject))
	}
	if s.opts.Session != "" {
		parts = append(parts, "--session", quote(s.opts.Session))
	}
	for _, arg := range args {
		parts = append(parts, quote(arg))
	}
	return strings.Join(parts, " ")
}

// quote wraps an argument a shell would split or expand in single quotes.
func quote(arg string) string {
	if arg != "" && !strings.ContainsAny(arg, " \t\n'\"$`\\*?[]{}()<>|&;!#~") {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// formatTable lays rows out in columns two spaces apart, the header first.
func formatTable(header []string, rows [][]string) string {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, row := range append([][]string{header}, rows...) {
		//nolint:errcheck // a strings.Builder does not fail a write
		_, _ = fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	//nolint:errcheck // a strings.Builder does not fail a write
	_ = tw.Flush()
	return b.String()
}

// formatSize renders a byte count in decimal units, to three significant digits at most.
func formatSize(size int64) string {
	const unit = 1000
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	var (
		suffixes = []string{"KB", "MB", "GB", "TB", "PB"}
		value    = float64(size) / unit
		i        int
	)
	for ; value >= 999.5 && i < len(suffixes)-1; i++ {
		value /= unit
	}
	if value < 99.95 {
		return fmt.Sprintf("%.1f %s", value, suffixes[i])
	}
	return fmt.Sprintf("%.0f %s", value, suffixes[i])
}

// formatOptionalSize renders a size that may be unknown.
func formatOptionalSize(size *int64) string {
	if size == nil {
		return "?"
	}
	return formatSize(*size)
}

// formatNotices renders the warnings block in the order given; "" for no notice.
func formatNotices(notices []model.Notice) string {
	if len(notices) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n" + styleWarningsTitle.Render(fmt.Sprintf("Warnings (%d)", len(notices))) + "\n")
	for _, notice := range notices {
		b.WriteString(styleWarning.Render("  ! "+notice.Message) + "\n")
		if notice.Hint != "" {
			b.WriteString(styleHint.Render("    → "+notice.Hint) + "\n")
		}
	}
	return b.String()
}

// formatShortID is the first eight characters of an id.
func formatShortID(id string) string {
	const shortIDLength = 8
	if len(id) > shortIDLength {
		return id[:shortIDLength]
	}
	return id
}

// formatCount is n and the noun, plural past one.
func formatCount(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
