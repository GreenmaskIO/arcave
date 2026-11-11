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

	"github.com/greenmaskio/arcave/internal/model"
)

// formatGenerateCompose is the compose file generate-compose wrote: where, for which compose
// project, the services it declares, and that it holds every environment value.
func formatGenerateCompose(response model.GenerateComposeResponse) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render(fmt.Sprintf("Wrote %s · compose project %s",
		response.Path, response.ComposeProject)) + "\n")
	fmt.Fprintf(&b, "  %-9s %s\n", "services", formatServiceNames(response.Services))
	b.WriteString("  It holds every environment value, secrets included.\n")
	return b.String()
}

// formatServiceNames names up to six services and counts the rest.
func formatServiceNames(names []string) string {
	const shown = 6
	if len(names) == 0 {
		return "-"
	}
	line := strings.Join(names[:min(len(names), shown)], ", ")
	if len(names) > shown {
		line += fmt.Sprintf(" +%d", len(names)-shown)
	}
	return line
}
