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

# Records the docs' terminal demos with vhs into docs/assets/demo/.
#
#   examples/demo/tapes/record.sh              # every tape
#   examples/demo/tapes/record.sh base-cycle   # one tape
#
# Each tape runs against a fresh copy of examples/demo, as compose project `myapp`
# on port 18080, so a demo you have running in examples/demo is left alone.
set -euo pipefail

TAPES_DIR=$(cd "$(dirname "$0")" && pwd)
DEMO_DIR=$(dirname "$TAPES_DIR")
REPO_ROOT=$(cd "$DEMO_DIR/../.." && pwd)
OUT_DIR="$REPO_ROOT/docs/assets/demo"
PROJECT=myapp
PORT=18080

for program in vhs docker; do
  command -v "$program" >/dev/null || { echo "record.sh: $program is required" >&2; exit 1; }
done

if [ $# -gt 0 ]; then
  TAPES=("$@")
else
  TAPES=()
  for tape in "$TAPES_DIR"/*.tape; do
    name=$(basename "$tape" .tape)
    [ "$name" = common ] || TAPES+=("$name")
  done
fi

make -C "$REPO_ROOT" build >/dev/null

WORK_DIR=$(mktemp -d)
cleanup() {
  (cd "$WORK_DIR/demo" 2>/dev/null && docker compose down -v --remove-orphans >/dev/null 2>&1) || true
  docker image ls --format '{{.Repository}}:{{.Tag}}' --filter "reference=arcave/$PROJECT-*" |
    xargs -r docker image rm >/dev/null 2>&1 || true
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

# A copy named `demo`, so nothing on screen names a home path or a temp dir.
mkdir -p "$WORK_DIR/demo"
tar -C "$DEMO_DIR" --exclude ./.arcave --exclude ./repo --exclude ./minio-data --exclude ./tapes \
  --exclude ./test.yaml -cf - . | tar -C "$WORK_DIR/demo" -xf -
sed -i.bak -e "s/^name: arcave-demo$/name: $PROJECT/" -e "s/\"8080:8080\"/\"$PORT:8080\"/" \
  "$WORK_DIR/demo/docker-compose.yaml"
rm "$WORK_DIR/demo/docker-compose.yaml.bak"
for tape in "$TAPES_DIR"/*.tape; do
  sed "s|^Source \"common.tape\"|Source \"$WORK_DIR/common.tape\"|" "$tape" >"$WORK_DIR/$(basename "$tape")"
done

export PATH="$REPO_ROOT/bin:$PATH"
export ARCAVE_PROJECT_NAME=$PROJECT
export PAGER=cat PSQL_PAGER=cat
unset ARCAVE_SESSION ARCAVE_FORMAT ARCAVE_PROGRESS ARCAVE_LOG_LEVEL

cd "$WORK_DIR/demo"
docker compose build -q
mkdir -p "$OUT_DIR"

for name in "${TAPES[@]}"; do
  echo "record.sh: recording $name" >&2
  # A clean start per tape: fresh volumes, data seeded by the app, an empty store.
  docker compose down -v --remove-orphans >/dev/null 2>&1
  docker image ls --format '{{.Repository}}:{{.Tag}}' --filter "reference=arcave/$PROJECT-*" |
    xargs -r docker image rm >/dev/null 2>&1 || true
  rm -rf .arcave
  docker compose up -d --wait >/dev/null 2>&1
  arcave init --name "$PROJECT" 2>/dev/null
  if [ -x "$TAPES_DIR/$name.setup.sh" ]; then
    "$TAPES_DIR/$name.setup.sh"
  fi
  (cd "$WORK_DIR/demo" && vhs -q "$WORK_DIR/$name.tape")
  mv "$WORK_DIR/demo/$name".* "$OUT_DIR/" 2>/dev/null || true
done

ls -lh "$OUT_DIR" >&2
