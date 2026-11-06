#!/usr/bin/env bash
# Copyright 2026 Greenmask Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# The gc tape starts from three cdc savepoints, each over a little more data, so a gc has
# restic snapshots to forget as well as image blobs.
set -euo pipefail

# A savepoint taken off screen: quiet unless it fails.
savepoint() {
  local log
  log=$(mktemp)
  arcave savepoint create "$1" --format json --progress log --log-level warn >/dev/null 2>"$log" ||
    { cat "$log" >&2; rm -f "$log"; return 1; }
  rm -f "$log"
}

sed -i.bak 's/^    backend: fullcopy$/    backend: cdc/' .arcave/config.yaml && rm .arcave/config.yaml.bak
grep -q '^    backend: cdc$' .arcave/config.yaml
for name in monday tuesday wednesday; do
  docker compose exec -T postgres psql -qU postgres demo \
    -c "insert into notes(text) select 'note ' || g from generate_series(1, 20000) g" >/dev/null
  savepoint "$name"
done
