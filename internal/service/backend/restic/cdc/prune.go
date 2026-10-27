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

package cdc

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
)

// totalPrunePattern is the line of a prune, a dry run's or not, naming what it removes, as restic
// 0.19.1 prints it: prune has no --json output, and forget --json suppresses it.
var totalPrunePattern = regexp.MustCompile(`^total prune:\s+\d+ blobs / ([0-9.]+) (B|KiB|MiB|GiB|TiB)$`)

// pruneUnits are the binary units restic formats a size in.
var pruneUnits = map[string]float64{
	"B":   1,
	"KiB": 1 << 10,
	"MiB": 1 << 20,
	"GiB": 1 << 30,
	"TiB": 1 << 40,
}

// newForgetArgs is forget of ids, then flags; an id that is not restic's is refused.
func newForgetArgs(ids []string, flags ...string) ([]string, error) {
	for _, id := range ids {
		if err := requireSnapshotID(id); err != nil {
			return nil, err
		}
	}
	args := append([]string{"forget"}, ids...)
	return append(args, flags...), nil
}

// readPruneTotal is what the prune whose output is out removes in objectID's repository; nil,
// warned of, when restic printed no total.
func readPruneTotal(ctx context.Context, out []byte, objectID string) (*int64, error) {
	freed, found, err := parsePruneTotal(out)
	if err != nil {
		return nil, err
	}
	if !found {
		zerolog.Ctx(ctx).Warn().Str("repository", objectID).Msg("restic printed no prune total")
		return nil, nil
	}
	return &freed, nil
}

// parsePruneTotal reads the bytes a prune's total line names, rounded as restic printed them;
// found is false when out holds no such line.
func parsePruneTotal(out []byte) (freed int64, found bool, err error) {
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		match := totalPrunePattern.FindStringSubmatch(strings.TrimSpace(scanner.Text()))
		if match == nil {
			continue
		}
		size, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			return 0, false, fmt.Errorf("parse prune total %q: %w", match[0], err)
		}
		return int64(math.Round(size * pruneUnits[match[2]])), true, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, false, fmt.Errorf("read prune output: %w", err)
	}
	return 0, false, nil
}
