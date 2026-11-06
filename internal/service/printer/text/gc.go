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

// garbageTally is one data kind summed over every compose project.
type garbageTally struct {
	found   int
	bytes   *int64
	deleted int
	freed   *int64
}

// formatGarbageCollect is a gc's findings by data kind, what it deleted, and what held it back.
func (s *Printer) formatGarbageCollect(result model.GarbageCollectResult) string {
	var (
		b       strings.Builder
		tallies = newGarbageTallies(result.Reports)
		rows    [][]string
		header  = []string{"  DATA", "FOUND", "SIZE"}
		title   = "Garbage collected"
	)
	if result.DryRun {
		title = "Garbage found (dry run)"
	} else {
		header = append(header, "DELETED", "FREED")
	}
	for _, dataKind := range model.GarbageDataKinds() {
		tally, ok := tallies[dataKind]
		if !ok {
			continue
		}
		row := []string{"  " + string(dataKind), strconv.Itoa(tally.found), formatOptionalSize(tally.bytes)}
		if !result.DryRun {
			row = append(row, strconv.Itoa(tally.deleted), formatOptionalSize(tally.freed))
		}
		rows = append(rows, row)
	}
	total := []string{"  total", strconv.Itoa(result.Total.Found), formatTotalBytes(result.Total)}
	if !result.DryRun {
		total = append(total, strconv.Itoa(result.Total.Deleted), formatOptionalSize(result.Total.FreedBytes))
	}
	rows = append(rows, total)

	b.WriteString(styleTitle.Render(fmt.Sprintf("%s · session %s", title, result.Session)) + "\n")
	b.WriteString(formatTable(header, rows))
	b.WriteString(formatNotices(newGarbageNotices(result)))
	if result.DryRun && result.Total.Found > 0 {
		b.WriteString("\n  delete it:  " + s.newCommand(false, "gc") + "\n")
	}
	return b.String()
}

// newGarbageTallies sums every report by its data kind.
func newGarbageTallies(reports []model.GarbageReport) map[model.GarbageDataKind]garbageTally {
	tallies := make(map[model.GarbageDataKind]garbageTally)
	for _, report := range reports {
		tally, seen := tallies[report.DataKind]
		if !seen {
			tally = garbageTally{
				bytes: new(int64),
				freed: new(int64),
			}
		}
		freed, deleted := new(int64), 0
		if report.Deletion != nil {
			freed, deleted = report.Deletion.FreedBytes(), len(report.Deletion.Deleted)
		}
		tallies[report.DataKind] = garbageTally{
			found:   tally.found + report.Summary.Found,
			bytes:   addSizes(tally.bytes, report.Summary.Bytes),
			deleted: tally.deleted + deleted,
			freed:   addSizes(tally.freed, freed),
		}
	}
	return tallies
}

// addSizes adds two sizes, unknown when either is.
func addSizes(a, b *int64) *int64 {
	if a == nil || b == nil {
		return nil
	}
	sum := *a + *b
	return &sum
}

// formatTotalBytes is the total size, marked when some size is not known.
func formatTotalBytes(total model.GarbageTotal) string {
	if !total.BytesComplete {
		return formatSize(total.Bytes) + "+"
	}
	return formatSize(total.Bytes)
}

// newGarbageNotices is what kept the gc from finishing: the warnings, what a collector could not
// judge or refused to, and the deletions that did not go through.
func newGarbageNotices(result model.GarbageCollectResult) []model.Notice {
	notices := make([]model.Notice, 0, len(result.Warnings))
	for _, warning := range result.Warnings {
		notices = append(notices, model.Notice{Message: warning})
	}
	for _, report := range result.Reports {
		where := string(report.DataKind)
		if report.ComposeProject != "" {
			where += " in " + string(report.ComposeProject)
		}
		switch {
		case report.Error != "":
			notices = append(notices, model.Notice{Message: where + ": failed: " + report.Error})
		case report.Summary.Aborted != "":
			notices = append(notices, model.Notice{Message: where + ": not judged: " + report.Summary.Aborted})
		}
		if report.Summary.Unjudged > 0 {
			notices = append(notices, model.Notice{
				Message: fmt.Sprintf("%s: %s could not be judged", where, formatCount(report.Summary.Unjudged, "object")),
			})
		}
		if report.Summary.Skipped > 0 {
			notices = append(notices, model.Notice{
				Message: fmt.Sprintf("%s: %s held back", where, formatCount(report.Summary.Skipped, "object")),
			})
		}
		if report.Deletion == nil {
			continue
		}
		if failed := len(report.Deletion.Failed); failed > 0 {
			notices = append(notices, model.Notice{
				Message: fmt.Sprintf("%s: %s failed", where, formatCount(failed, "deletion")),
			})
		}
		if report.Deletion.NotRun > 0 {
			notices = append(notices, model.Notice{
				Message: fmt.Sprintf("%s: %s not run", where, formatCount(report.Deletion.NotRun, "deletion")),
			})
		}
	}
	return notices
}
