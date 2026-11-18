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

// formatUp is what an up built and, when forced over a present compose project, what it dropped.
func (s *Printer) formatUp(response model.UpResult) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Up from "+
		s.formatSavepointTitle(response.RolledBackTo, "2006-01-02 15:04:05")) + "\n")
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
	b.WriteString(formatCaptureRows(nil, rows))
	if response.Discarded != nil {
		b.WriteString(formatCaptureLine("dropped", []string{formatDiscarded(*response.Discarded)}, []int{0}) + "\n")
	}
	var notices []model.Notice
	if len(response.WritableLayers) > 0 {
		notices = append(notices, model.Notice{
			Message: "the containers run on the savepoint's writable layers",
			Hint:    "`docker compose up` of your compose file recreates them on its images and drops those layers",
		})
	}
	b.WriteString(formatNotices(notices))
	return b.String()
}

// formatDown is whose environment a down took off the daemon, what went with it, and its notices.
func (s *Printer) formatDown(response model.DownResult) string {
	var b strings.Builder
	discarded := response.Discarded
	if len(discarded.Containers)+len(discarded.Volumes)+len(discarded.Networks) == 0 {
		b.WriteString(styleTitle.Render("Nothing of "+response.ComposeProject.String()+" to take down") + "\n")
	} else {
		b.WriteString(styleTitle.Render("Took down "+response.ComposeProject.String()) + "\n")
	}
	if response.Deployment != nil {
		b.WriteString(formatCaptureLine("deployed", []string{formatDeployment(*response.Deployment)}, []int{0}) + "\n")
	}
	b.WriteString(formatCaptureLine("dropped", []string{formatDiscarded(discarded)}, []int{0}) + "\n")
	if len(response.RemovedTags) > 0 {
		b.WriteString(formatCaptureLine("layers", []string{formatCount(len(response.RemovedTags), "tag") + " removed"}, []int{0}) + "\n")
	}
	notices := response.Notices
	if response.Forced {
		notices = append([]model.Notice{{Message: "forced: whose environment it was was not checked"}}, notices...)
	}
	b.WriteString(formatNotices(notices))
	return b.String()
}

// formatDeployment names the savepoint a deployment built and the session it came from.
func formatDeployment(deployment model.SavepointDeployment) string {
	return fmt.Sprintf("%s · session %s", deployment.Savepoint, deployment.Session)
}

// formatDiscarded counts what was dropped, "nothing" for none.
func formatDiscarded(discarded model.DiscardedResources) string {
	var parts []string
	for _, kind := range []struct {
		noun string
		n    int
	}{{"container", len(discarded.Containers)}, {"volume", len(discarded.Volumes)}, {"network", len(discarded.Networks)}} {
		if kind.n > 0 {
			parts = append(parts, formatCount(kind.n, kind.noun))
		}
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, ", ")
}
