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
	"cmp"
	"fmt"
	"strconv"
	"strings"

	"github.com/greenmaskio/arcave/internal/model"
)

// captureRow is one line of a summary: a kind of unit and its cells, aligned under the header.
type captureRow struct {
	kind  string
	cells []string
}

// formatSavepoint renders a created savepoint: what it holds, what it leaves out, how to go back.
func (s *Printer) formatSavepoint(savepoint model.Savepoint) string {
	var b strings.Builder
	noun := "Savepoint "
	if savepoint.IsSafetySnapshot() {
		noun = "Safety snapshot "
	}
	title := noun + s.formatSavepointTitle(savepoint, "15:04:05") + " · stored " + formatSize(newStoredBytes(savepoint))
	b.WriteString(styleTitle.Render(title) + "\n")
	b.WriteString(formatCaptureRows([]string{"count", "size", "stored"}, newCaptureRows(savepoint)))
	b.WriteString(formatNotices(savepoint.Notices()))
	b.WriteString("\n  roll back:  " + s.newCommand(true, "rollback", newSavepointRef(savepoint)) + "\n")
	return b.String()
}

// formatSavepoints is a listing as a table, newest as the listing ordered it.
func (s *Printer) formatSavepoints(savepoints []model.Savepoint) string {
	if len(savepoints) == 0 {
		return "No savepoints.\n"
	}
	return s.formatSavepointTable(savepoints)
}

// formatSavepointTable is savepoints as a table, in the order given.
func (s *Printer) formatSavepointTable(savepoints []model.Savepoint) string {
	rows := make([][]string, 0, len(savepoints))
	for _, savepoint := range savepoints {
		rows = append(rows, []string{
			formatSavepointName(savepoint),
			savepoint.ID.String(),
			s.formatTime(savepoint.CreatedAt),
			strconv.Itoa(len(savepoint.Stats)),
			strconv.Itoa(len(savepoint.WritableLayers)),
			formatSize(newStoredBytes(savepoint)),
			formatZeroDash(len(savepoint.Notices())),
		})
	}
	return formatTable([]string{"NAME", "ID", "CREATED", "VOLUMES", "LAYERS", "STORED", "NOT COVERED"}, rows)
}

// formatRollback is what a rollback restored, and what is left to know about it.
func (s *Printer) formatRollback(response model.RollbackResult) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render(s.formatRolledBackTo(response.RolledBackTo)) + "\n")
	rows := []captureRow{{
		kind:  "volumes",
		cells: []string{strconv.Itoa(len(response.Restored))},
	}}
	if len(response.WritableLayers) > 0 {
		rows = append(rows, captureRow{
			kind:  "layers",
			cells: []string{strconv.Itoa(len(response.WritableLayers))},
		})
	}
	// Whether one was taken, not which: its id is in the undo command below.
	safety := "none"
	if response.SafetySnapshot != "" {
		safety = "taken"
	}
	b.WriteString(formatCaptureRows(nil, rows))
	// Below the counts, not aligned with them: a word is no number.
	b.WriteString(formatCaptureLine("safety", []string{safety}, []int{0}) + "\n")

	b.WriteString(formatNotices(response.Notices()))
	if response.SafetySnapshot != "" {
		b.WriteString("\n  undo it:  " + s.newCommand(true, "rollback", response.SafetySnapshot.String()) + "\n")
	}
	return b.String()
}

// formatRemove is the savepoints a remove took, or would take.
func (s *Printer) formatRemove(response model.RemoveResult) string {
	if len(response.Removed) == 0 {
		return "Nothing to remove.\n"
	}
	verb := "Removed"
	if response.DryRun {
		verb = "Would remove"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s:\n", verb, formatCount(len(response.Removed), "savepoint"))
	for _, savepoint := range response.Removed {
		fmt.Fprintf(&b, "  %s · %s · %s\n", formatSavepointName(savepoint), savepoint.ID,
			s.formatTime(savepoint.CreatedAt))
	}
	return b.String()
}

// formatRolledBackTo is the rollback's title: the savepoint it restored, or for a safety snapshot
// the state it holds — the one before the rollback it was taken before, at that rollback's time.
func (s *Printer) formatRolledBackTo(savepoint model.Savepoint) string {
	const layout = "2006-01-02 15:04:05"
	if takenBefore := savepoint.TakenBeforeRollbackTo; takenBefore != nil && savepoint.IsSafetySnapshot() {
		return "Rolled back to the state " + formatTakenBefore(*takenBefore) + " · " +
			s.formatSavepointTitle(model.Savepoint{ID: savepoint.ID, CreatedAt: savepoint.CreatedAt}, layout)
	}
	return "Rolled back to " + s.formatSavepointTitle(savepoint, layout)
}

// formatSavepointTitle is "<name> · <id> · <time>", the name left out when none.
func (s *Printer) formatSavepointTitle(savepoint model.Savepoint, layout string) string {
	parts := make([]string, 0, 3)
	if savepoint.Name != "" {
		parts = append(parts, savepoint.Name)
	}
	parts = append(parts,
		savepoint.ID.String(),
		savepoint.CreatedAt.In(s.opts.Location).Format(layout))
	return strings.Join(parts, " · ")
}

// newCaptureRows sums up a savepoint by kind of unit: volumes always, layers and images when it
// holds any. Kinds are not totalled: a layer's archive holds its image's layers.
func newCaptureRows(savepoint model.Savepoint) []captureRow {
	var size, stored int64
	for _, stat := range savepoint.Stats {
		size += stat.SizeBytes
		stored += stat.StoredBytes
	}
	rows := []captureRow{newCaptureRow("volumes", len(savepoint.Stats), size, stored)}
	if len(savepoint.WritableLayers) > 0 {
		var layerSize, layerStored int64
		for _, layer := range savepoint.WritableLayers {
			layerSize += layer.SizeBytes
			layerStored += layer.StoredBytes
		}
		rows = append(rows, newCaptureRow("layers", len(savepoint.WritableLayers), layerSize, layerStored))
	}
	if len(savepoint.Images) > 0 {
		var imageSize, imageStored int64
		for _, image := range savepoint.Images {
			imageSize += image.SizeBytes
			imageStored += image.StoredBytes
		}
		rows = append(rows, newCaptureRow("images", len(savepoint.Images), imageSize, imageStored))
	}
	return rows
}

// newCaptureRow builds a kind's row of count, size and stored bytes.
func newCaptureRow(kind string, count int, size, stored int64) captureRow {
	storedCell := "0"
	if stored != 0 {
		storedCell = formatSize(stored)
	}
	return captureRow{
		kind:  kind,
		cells: []string{strconv.Itoa(count), formatSize(size), storedCell},
	}
}

// formatCaptureRows aligns the rows under the header, if any: the kind left, the cells right.
func formatCaptureRows(header []string, rows []captureRow) string {
	widths := make([]int, len(header))
	for i, title := range header {
		widths[i] = len(title)
	}
	for _, row := range rows {
		for i, cell := range row.cells {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], len(cell))
		}
	}
	var b strings.Builder
	if len(header) > 0 {
		b.WriteString(formatCaptureLine("", header, widths) + "\n")
	}
	for _, row := range rows {
		b.WriteString(formatCaptureLine(row.kind, row.cells, widths) + "\n")
	}
	return b.String()
}

// formatCaptureLine renders one aligned line of a summary.
func formatCaptureLine(kind string, cells []string, widths []int) string {
	line := fmt.Sprintf("  %-8s", kind)
	for i, cell := range cells {
		line += fmt.Sprintf("  %*s", widths[i], cell)
	}
	return strings.TrimRight(line, " ")
}

// newSavepointRef is what rollback takes the savepoint by: its name, else its id.
func newSavepointRef(savepoint model.Savepoint) string {
	if savepoint.Name != "" {
		return savepoint.Name
	}
	return savepoint.ID.String()
}

// formatSavepointName is the name, "-" for none; a safety snapshot is named by the rollback it was
// taken before, and marked.
func formatSavepointName(savepoint model.Savepoint) string {
	if !savepoint.IsSafetySnapshot() {
		return cmp.Or(savepoint.Name, "-")
	}
	name := cmp.Or(savepoint.Name, "-")
	if takenBefore := savepoint.TakenBeforeRollbackTo; takenBefore != nil {
		name = formatTakenBefore(*takenBefore)
	}
	return name + " (safety)"
}

// formatTakenBefore is "before rollback to <savepoint>": the target as named when the safety
// snapshot was taken, by its full id when it had no name.
func formatTakenBefore(target model.SavepointRef) string {
	return "before rollback to " + target.String()
}

// newStoredBytes is what the savepoint wrote to the store, every kind of unit together.
func newStoredBytes(savepoint model.Savepoint) int64 {
	var stored int64
	for _, stat := range savepoint.Stats {
		stored += stat.StoredBytes
	}
	for _, layer := range savepoint.WritableLayers {
		stored += layer.StoredBytes
	}
	for _, image := range savepoint.Images {
		stored += image.StoredBytes
	}
	return stored
}

func formatZeroDash(n int) string {
	if n == 0 {
		return "-"
	}
	return strconv.Itoa(n)
}
