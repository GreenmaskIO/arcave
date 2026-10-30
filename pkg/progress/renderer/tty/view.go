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

package tty

import (
	"fmt"
	"slices"
	"strings"
	"time"

	progressbar "charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/lipgloss/v2"
	lgtree "charm.land/lipgloss/v2/tree"
	"github.com/charmbracelet/x/ansi"

	"github.com/greenmaskio/arcave/pkg/progress"
	"github.com/greenmaskio/arcave/pkg/progress/renderer/internal/format"
)

const (
	// indentWidth is what lipgloss/tree puts before a child per level: "├── ".
	indentWidth  = 4
	defaultWidth = 80
	// minLabelWidth is as far as a label is cut before the message is.
	minLabelWidth = 8
	// minMsgWidth is the narrowest a cut message is kept at; narrower, it is dropped.
	minMsgWidth = 3
	// minLineWidth keeps a line on a narrow terminal deep in the tree.
	minLineWidth = 20
	// foldDelay is how long a live frame keeps a quiet success open after it ends, so a quick
	// step's lines are seen.
	foldDelay = 1500 * time.Millisecond
	// maxFinalWarnings is how many warnings a final frame writes per node; the rest are counted.
	maxFinalWarnings = 5
)

var (
	styleSucceeded = lipgloss.NewStyle().Foreground(lipgloss.Green)
	styleFailed    = lipgloss.NewStyle().Foreground(lipgloss.Red)
	styleWarned    = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	styleFaint     = lipgloss.NewStyle().Faint(true)
	styleBranch    = styleFaint.PaddingRight(1)

	spinnerFrames = spinner.MiniDot.Frames
	fullBar       = newBar(20)
	shortBar      = newBar(10)
)

// widgetDetail is how much of a byte widget a line has room for.
type widgetDetail int

const (
	// widgetFull is a full bar, the count and the speed.
	widgetFull widgetDetail = iota
	// widgetShort is a short bar and the count.
	widgetShort
	// widgetCount is the count alone.
	widgetCount
)

// frame is what a render depends on besides the tree.
type frame struct {
	now    time.Time
	width  int
	height int
	spin   int
	// final renders every node whole but a quiet success below the roots: nothing folded or cut,
	// as StopOptions say.
	final bool
	StopOptions
	// minColumn is as far left as a live frame may put the right edge of the elapsed time.
	minColumn int
}

// item is a node laid out: its line, not yet aligned, the lines under it and its children shown.
type item struct {
	// left is the node's line up to its elapsed time.
	left string
	// elapsed is empty for a node cancelled before it ran.
	elapsed  string
	depth    int
	details  []string
	children []*item
	// foldable is a finished node the height may hide: neither running nor failed.
	foldable bool
	// folded counts the children hidden for the height, shown as one line.
	folded int
}

// render draws every root's tree and returns the column the elapsed times end at; a live frame
// folds the earliest finished children until it fits height-1.
func render(t *tree, f frame) (string, int) {
	if f.width <= 0 {
		f.width = defaultWidth
	}
	top := &item{children: newItems(t, t.roots, 0, f, false)}
	if !f.final && f.height > 0 {
		// top has no line of its own, which newItemHeight counts.
		top, _ = fold(top, newItemHeight(top)-f.height)
	}
	var (
		column = newColumn(top.children, f)
		parts  = make([]string, 0, len(top.children)+1)
	)
	if top.folded > 0 {
		parts = append(parts, newFoldedLine(top.folded))
	}
	for _, root := range top.children {
		parts = append(parts, fmt.Sprint(toTreeNode(root, column, true)))
	}
	return trimLines(strings.Join(parts, "\n")), column
}

// newItems lays out ids at depth; under a batch, and below the roots' children in an essential
// frame, what it has nothing to say about is not shown at all, the batch's count speaking for it.
func newItems(t *tree, ids []progress.StepID, depth int, f frame, underBatch bool) []*item {
	items := make([]*item, 0, len(ids))
	for _, id := range ids {
		current := t.nodes[id]
		if (underBatch || (f.Essential && depth > 1)) && isHiddenUnderBatch(t, current, f) {
			continue
		}
		items = append(items, newItem(t, current, depth, f))
	}
	return items
}

func newItem(t *tree, current *node, depth int, f frame) *item {
	var (
		budget   = max(f.width-depth*indentWidth, minLineWidth)
		elapsed  = newElapsedText(current, f)
		details  []string
		children []*item
	)
	if !isCollapsed(t, current, depth, f) {
		details = newDetailLines(current, f, budget, depth)
		children = newItems(t, current.children, depth+1, f, isBatch(current))
	}
	return &item{
		left:     newLeft(current, f, newRoom(budget, elapsed)),
		elapsed:  elapsed,
		depth:    depth,
		details:  details,
		children: children,
		foldable: current.last.Status.IsTerminal() && !hasFailure(t, current),
	}
}

// isCollapsed reports a quiet success shown by its line alone: a final frame's below the roots, a
// live frame's once it has ended foldDelay ago.
func isCollapsed(t *tree, current *node, depth int, f frame) bool {
	if !isQuiet(t, current, f) {
		return false
	}
	if f.final {
		return depth > 0
	}
	return f.now.Sub(current.endedAt) >= foldDelay
}

// isQuiet reports a success with nothing to say in its subtree: no warning, no failure.
func isQuiet(t *tree, current *node, f frame) bool {
	return current.last.Status == progress.StatusSucceeded && !hasNotice(t, current, f)
}

// isHiddenUnderBatch reports a quiet success, or a cancellation that stopped nothing under way.
func isHiddenUnderBatch(t *tree, current *node, f frame) bool {
	return isQuiet(t, current, f) || hasQuietCancel(t, current, f)
}

// hasQuietCancel reports a node cancelled before it ran: no time taken, no byte moved, nothing to
// say in its subtree.
func hasQuietCancel(t *tree, current *node, f frame) bool {
	sample := current.last.Sample
	return current.last.Status == progress.StatusCancelled &&
		formatElapsed(sample.Elapsed) == formatElapsed(0) &&
		(sample.Bytes == nil || sample.Bytes.Done == 0) &&
		!hasNoticeBesidesCancel(t, current, f)
}

func hasNotice(t *tree, current *node, f frame) bool {
	return current.last.Status == progress.StatusCancelled || hasNoticeBesidesCancel(t, current, f)
}

// hasNoticeBesidesCancel is hasNotice with the node's own cancellation left out.
func hasNoticeBesidesCancel(t *tree, current *node, f frame) bool {
	warned := slices.ContainsFunc(current.warnings, func(w warning) bool { return isShown(w, f) }) ||
		slices.ContainsFunc(current.resources, func(line resourceLine) bool { return line.warned })
	return warned || hasFailure(t, current) ||
		slices.ContainsFunc(current.children, func(id progress.StepID) bool { return hasNotice(t, t.nodes[id], f) })
}

// isShown reports whether the frame writes w: an essential one leaves out what the result repeats.
func isShown(w warning, f frame) bool {
	return !f.Essential || !w.inResult
}

func hasFailure(t *tree, current *node) bool {
	return current.last.Status == progress.StatusFailed ||
		slices.ContainsFunc(current.children, func(id progress.StepID) bool { return hasFailure(t, t.nodes[id]) })
}

func isBatch(current *node) bool {
	return current.open.Step.Type == progress.StepTypeWorkflow &&
		slices.Contains(current.open.Measures, progress.MeasureItems)
}

// newColumn is where the elapsed times end: right of the widest node line, minColumn in a live
// frame, the width at most.
func newColumn(items []*item, f frame) int {
	column := newLinesWidth(items)
	if !f.final {
		column = max(column, f.minColumn)
	}
	return min(column, f.width)
}

// newLinesWidth is the widest node line of items and their children, indent and time included.
func newLinesWidth(items []*item) int {
	var width int
	for _, it := range items {
		line := it.depth*indentWidth + ansi.StringWidth(it.left)
		if it.elapsed != "" {
			line += 2 + ansi.StringWidth(it.elapsed)
		}
		width = max(width, line, newLinesWidth(it.children))
	}
	return width
}

// newRoom is what a node line at budget leaves the label, the widget and the message: the icon
// and the elapsed time take the rest.
func newRoom(budget int, elapsed string) int {
	room := budget - 2
	if elapsed != "" {
		room -= 2 + ansi.StringWidth(elapsed)
	}
	return room
}

// newNodeLine is `<icon> <label>  <widget>  <msg>` with the elapsed time right-aligned at column.
func newNodeLine(it *item, column int) string {
	if it.elapsed == "" {
		return it.left
	}
	pad := max(column-it.depth*indentWidth-ansi.StringWidth(it.left)-ansi.StringWidth(it.elapsed), 2)
	return it.left + strings.Repeat(" ", pad) + styleFaint.Render(it.elapsed)
}

// newLeft is the line up to the elapsed time, fit into room unless the frame is final. What does
// not fit gives way: the label to minLabelWidth, the message, the widget, the label.
func newLeft(current *node, f frame, room int) string {
	label := formatLabel(current.open.Step)
	if f.final {
		return joinLeft(current, f, label, newWidget(current, f, widgetFull), current.msg)
	}
	widgets := []string{
		newWidget(current, f, widgetFull),
		newWidget(current, f, widgetShort),
		newWidget(current, f, widgetCount),
		"",
	}
	for _, widget := range widgets {
		msg := current.msg
		if over := newLeftWidth(label, widget, msg) - room; over > 0 {
			label = truncate(label, max(ansi.StringWidth(label)-over, minLabelWidth))
		}
		if over := newLeftWidth(label, widget, msg) - room; over > 0 {
			msg = truncate(msg, ansi.StringWidth(msg)-over)
			if ansi.StringWidth(msg) < minMsgWidth {
				msg = ""
			}
		}
		if newLeftWidth(label, widget, msg) <= room {
			return joinLeft(current, f, label, widget, msg)
		}
		label = formatLabel(current.open.Step)
	}
	return joinLeft(current, f, truncate(label, max(room, 1)), "", "")
}

func joinLeft(current *node, f frame, label, widget, msg string) string {
	left := newIcon(current.last.Status, f.spin) + " " + label
	if widget != "" {
		left += "  " + widget
	}
	if msg != "" {
		left += "  " + msg
	}
	return left
}

func newLeftWidth(label, widget, msg string) int {
	width := ansi.StringWidth(label)
	if widget != "" {
		width += 2 + ansi.StringWidth(widget)
	}
	if msg != "" {
		width += 2 + ansi.StringWidth(msg)
	}
	return width
}

func newIcon(status progress.Status, spin int) string {
	switch status {
	case progress.StatusSucceeded:
		return styleSucceeded.Render("✔")
	case progress.StatusFailed:
		return styleFailed.Render("✘")
	case progress.StatusCancelled:
		return styleFaint.Render("⊘")
	default:
		return spinnerFrames[spin%len(spinnerFrames)]
	}
}

// newElapsedText is the node's elapsed time, none for one cancelled before it took any.
func newElapsedText(current *node, f frame) string {
	elapsed := formatElapsed(newElapsed(current, f))
	if current.last.Status == progress.StatusCancelled && elapsed == formatElapsed(0) {
		return ""
	}
	return elapsed
}

// newElapsed is the time a running node has run so far, or the time a finished one took.
func newElapsed(current *node, f frame) time.Duration {
	if current.last.Status.IsTerminal() {
		return current.last.Sample.Elapsed
	}
	return max(f.now.Sub(current.startedAt), 0)
}

// newWidget renders the node's measures, a byte widget to detail and a batch's tally.
func newWidget(current *node, f frame, detail widgetDetail) string {
	var (
		sample  = current.last.Sample
		running = !current.last.Status.IsTerminal()
		parts   []string
	)
	if slices.Contains(current.open.Measures, progress.MeasureBytes) && sample.Bytes != nil {
		if !running {
			detail = widgetCount
		}
		parts = append(parts, newBytesWidget(*sample.Bytes, newElapsed(current, f), detail, !running))
	}
	if slices.Contains(current.open.Measures, progress.MeasureItems) && sample.Items != nil {
		parts = append(parts, newItemsWidget(*sample.Items))
	}
	return strings.Join(parts, "  ")
}

// newBytesWidget is the count, with a bar while the total is known and not exceeded and the
// average speed as detail allows; and what was stored and reused, once known. A node finished
// with every byte of its total names the count alone.
func newBytesWidget(bytes progress.Bytes, elapsed time.Duration, detail widgetDetail, finished bool) string {
	var (
		parts   []string
		whole   = finished && bytes.Done == bytes.Total
		known   = bytes.Total > 0 && bytes.Done <= bytes.Total && !whole
		percent = float64(bytes.Done) / float64(max(bytes.Total, 1))
	)
	switch {
	case known && detail == widgetFull:
		parts = append(parts, fullBar.ViewAs(percent))
	case known && detail == widgetShort:
		parts = append(parts, shortBar.ViewAs(percent))
	}
	if known {
		parts = append(parts, format.Size(bytes.Done)+"/"+format.Size(bytes.Total))
	} else {
		parts = append(parts, format.Size(bytes.Done))
	}
	if detail == widgetFull && elapsed > 0 {
		parts = append(parts, format.Size(int64(float64(bytes.Done)/elapsed.Seconds()))+"/s")
	}
	if bytes.Stored != 0 {
		parts = append(parts, "stored "+format.Size(bytes.Stored))
	}
	if bytes.Reused != 0 {
		parts = append(parts, "reused "+format.Size(bytes.Reused))
	}
	return strings.Join(parts, " ")
}

func newItemsWidget(items progress.Items) string {
	widget := fmt.Sprint(items.Done)
	if items.Total != 0 {
		widget = fmt.Sprintf("%d/%d", items.Done, items.Total)
	}
	if items.Failed != 0 {
		widget += " " + styleFailed.Render(fmt.Sprintf("✘%d", items.Failed))
	}
	if items.Cancelled != 0 {
		widget += " " + styleFaint.Render(fmt.Sprintf("⊘%d", items.Cancelled))
	}
	return widget
}

// formatLabel is the kind, then the name or the id.
func formatLabel(step progress.Step) string {
	switch {
	case step.Name != "":
		return string(step.Kind) + " " + step.Name
	case step.ID != "":
		return string(step.Kind) + " " + step.ID
	default:
		return string(step.Kind)
	}
}

func formatResource(resource progress.Resource) string {
	if resource.Name != "" {
		return resource.Name
	}
	return resource.ID
}

// formatElapsed renders 0.4s, 12.3s, 1m02s.
func formatElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	d = d.Round(time.Second)
	return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
}

func truncate(s string, width int) string {
	return ansi.Truncate(s, width, "…")
}

func newBar(width int) progressbar.Model {
	return progressbar.New(
		progressbar.WithWidth(width),
		progressbar.WithoutPercentage(),
		progressbar.WithDefaultBlend(),
		progressbar.WithFillCharacters('█', '░'),
	)
}

// newDetailLines returns the lines under the node's own: one per resource, warning and error, each
// cut to budget unless the frame is final.
func newDetailLines(current *node, f frame, budget int, depth int) []string {
	var (
		lines  []string
		warned int
	)
	cut := func(line string) string {
		if f.final {
			return line
		}
		return truncate(line, budget)
	}
	// keep counts a warning and reports whether the frame still has room for it.
	keep := func() bool {
		warned++
		return !f.final || warned <= maxFinalWarnings
	}
	for _, line := range current.resources {
		switch {
		case !line.warned && f.Essential:
		case !line.warned:
			lines = append(lines, cut("  "+formatResource(line.resource)+"  "+line.msg))
		case keep():
			lines = append(lines, styleWarned.Render(cut("  "+formatResource(line.resource)+"  "+line.msg)))
		}
	}
	for _, w := range current.warnings {
		if isShown(w, f) && keep() {
			lines = append(lines, styleWarned.Render(cut("  ! "+w.msg)))
		}
	}
	if hidden := warned - maxFinalWarnings; f.final && hidden > 0 {
		lines = append(lines, styleFaint.Render(newMoreLine(hidden, f.MoreHint)))
	}
	if current.last.Error != "" && (depth > 0 || !f.OmitRootError) {
		lines = append(lines, styleFailed.Render(cut("  error: "+current.last.Error)))
	}
	return lines
}

// newMoreLine counts the warnings a final frame left out of a node, hint after.
func newMoreLine(hidden int, hint string) string {
	line := fmt.Sprintf("  … %d more", hidden)
	if hint != "" {
		line += ", " + hint
	}
	return line
}

// fold returns it with its earliest foldable children folded, then its descendants', until excess
// lines are saved, and how many it saved. A fold that saves nothing — one line traded for the
// "… more finished" line — is not made.
func fold(it *item, excess int) (*item, int) {
	if excess <= 0 {
		return it, 0
	}
	var (
		children = make([]*item, 0, len(it.children))
		folded   int
		saved    int
	)
	for _, child := range it.children {
		if saved < excess && child.foldable {
			saved += newItemHeight(child)
			folded++
			continue
		}
		children = append(children, child)
	}
	if folded > 0 {
		// The "… more finished" line takes one of the lines saved.
		saved--
	}
	if saved <= 0 {
		children, folded, saved = slices.Clone(it.children), 0, 0
	}
	for i, child := range children {
		if saved >= excess {
			break
		}
		var childSaved int
		children[i], childSaved = fold(child, excess-saved)
		saved += childSaved
	}
	return &item{
		left:     it.left,
		elapsed:  it.elapsed,
		depth:    it.depth,
		details:  it.details,
		children: children,
		foldable: it.foldable,
		folded:   folded,
	}, saved
}

func newItemHeight(it *item) int {
	height := 1 + len(it.details)
	if it.folded > 0 {
		height++
	}
	for _, child := range it.children {
		height += newItemHeight(child)
	}
	return height
}

// toTreeNode turns an item into what lipgloss/tree renders, its time at column: a leaf, or a
// tree of its children.
func toTreeNode(it *item, column int, root bool) any {
	value := strings.Join(append([]string{newNodeLine(it, column)}, it.details...), "\n")
	if len(it.children) == 0 && it.folded == 0 && !root {
		return value
	}
	node := lgtree.Root(value)
	if root {
		node = node.EnumeratorStyle(styleBranch).IndenterStyle(styleBranch)
	}
	if it.folded > 0 {
		node = node.Child(newFoldedLine(it.folded))
	}
	for _, child := range it.children {
		node = node.Child(toTreeNode(child, column, false))
	}
	return node
}

func newFoldedLine(n int) string {
	return styleFaint.Render(fmt.Sprintf("… %d more finished", n))
}

// trimLines drops the spaces lipgloss/tree pads a multi-line item's shorter lines with.
func trimLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}
