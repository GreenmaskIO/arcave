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

package test

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/pkg/progress"
)

// postgresHooksSpec is postgres, its WAL on a volume of its own, held by mode %[2]s around two hook
// scripts, under a bank that never stops transferring; the bank itself is captured live.
const postgresHooksSpec = `
name: %[1]s
services:
  db:
    image: postgres:16
    environment:
      POSTGRES_HOST_AUTH_METHOD: trust
      POSTGRES_INITDB_WALDIR: /var/lib/postgresql/wal
    labels:
      arcave.capture.mode: %[2]s
      arcave.image.capture: "false"
      arcave.hook.pre-capture: /hooks/pre-capture.sh
      arcave.hook.pre-capture.user: postgres
      arcave.hook.pre-capture.timeout: 2m
      arcave.hook.post-capture: /hooks/post-capture.sh
      arcave.hook.post-capture.user: postgres
    healthcheck:
      test: ["CMD", "pg_isready", "-h", "127.0.0.1", "-U", "postgres"]
      interval: 1s
      retries: 60
    volumes:
      - pgdata:/var/lib/postgresql/data
      - pgwal:/var/lib/postgresql/wal
  bank:
    image: postgres:16
    labels:
      arcave.capture.mode: live
      arcave.image.capture: "false"
      arcave.writable-layer.capture: "false"
    stop_signal: SIGKILL
    depends_on:
      db:
        condition: service_healthy
    command: [sh, -c, "while true; do psql -h db -U postgres -qAtc 'select transfer()' >/dev/null 2>&1; done"]
volumes:
  pgdata: {}
  pgwal: {}
`

// bankSchema is ten accounts of 1000 each, a ledger of the transfers between them, and the log the
// hooks write.
const bankSchema = `
create table accounts (id int primary key, balance bigint not null);
insert into accounts select g, 1000 from generate_series(1, 10) g;
create table ledger (id bigserial primary key, src int not null, dst int not null, amount int not null);
create table hook_log (id serial primary key, phase text not null, lsn pg_lsn);
create function transfer() returns void language plpgsql as $f$
declare s int := 1 + floor(random() * 10); d int := 1 + floor(random() * 10); a int := 1 + floor(random() * 50);
begin
  update accounts set balance = balance - a where id = s;
  update accounts set balance = balance + a where id = d;
  insert into ledger (src, dst, amount) values (s, d, a);
end $f$;
`

// hookScripts writes the hooks of postgresHooksSpec: pre-capture logs itself, checkpoints and logs
// the checkpoint's redo point; post-capture logs itself.
const hookScripts = `mkdir -p /hooks
cat > /hooks/pre-capture.sh <<'EOF'
#!/bin/sh
set -eu
psql -v ON_ERROR_STOP=1 -qc "insert into hook_log (phase) values ('pre')"
psql -v ON_ERROR_STOP=1 -qc "CHECKPOINT"
psql -v ON_ERROR_STOP=1 -qc "insert into hook_log (phase, lsn) select 'checkpoint', redo_lsn from pg_control_checkpoint()"
EOF
cat > /hooks/post-capture.sh <<'EOF'
#!/bin/sh
set -eu
psql -v ON_ERROR_STOP=1 -qc "insert into hook_log (phase) values ('post')"
EOF
chmod 755 /hooks/*.sh`

// unbalancedAccounts counts the accounts whose balance the ledger does not explain.
const unbalancedAccounts = `select count(*) from accounts a where balance <> 1000
  + coalesce((select sum(amount) from ledger where dst = a.id), 0)
  - coalesce((select sum(amount) from ledger where src = a.id), 0)`

// redoStarts is the line postgres logs when it replays WAL on start, and the LSN it starts from.
var redoStarts = regexp.MustCompile(`redo starts at ([0-9A-F]+/[0-9A-F]+)`)

// hookedPostgres is a savepoint of postgresHooksSpec taken under load, and what it left behind.
type hookedPostgres struct {
	*hooksEnv
	created arcaveRun
	// entry and held are postgres before and after the capture.
	entry, held container.InspectResponse
	// logBefore is hook_log after the capture.
	logBefore string
}

// restoredPostgres is what a rollback to a hookedPostgres brought back.
type restoredPostgres struct {
	run                                     arcaveRun
	logAfter, checkpoint, total, unbalanced string
	amcheck                                 execOutcome
	redos                                   [][]string
}

// A rollback to a postgres paused around its hooks recovers from the pre-capture hook's checkpoint,
// consistent, and the capture never restarted it.
func TestRollback_PostgresPausedAroundHooks(t *testing.T) {
	t.Parallel()

	// Arrange
	sp := newHookedPostgres(t, "pause")

	// Act
	restored := sp.rollBack(t)

	// Assert
	sp.assertConsistent(t, restored)
	require.Len(t, restored.redos, 1, "the restored copy replayed once")
	assert.GreaterOrEqual(t, parseLSN(t, restored.redos[0][1]), parseLSN(t, restored.checkpoint),
		"the recovery starts no earlier than the hook's checkpoint")
	assert.Equal(t, sp.entry.State.StartedAt, sp.held.State.StartedAt, "the capture did not restart postgres")
	assert.Equal(t, sp.entry.RestartCount, sp.held.RestartCount)
	assert.False(t, sp.held.State.Paused, "the capture resumed what it froze")
}

// A rollback to a postgres stopped around its hooks comes up consistent from a clean shutdown,
// replaying nothing.
func TestRollback_PostgresStoppedAroundHooks(t *testing.T) {
	t.Parallel()

	// Arrange
	sp := newHookedPostgres(t, "stop")

	// Act
	restored := sp.rollBack(t)

	// Assert
	sp.assertConsistent(t, restored)
	assert.Empty(t, restored.redos, "a copy after a clean stop replays nothing")
}

// newHookedPostgres brings postgresHooksSpec up under mode, lets the bank run, takes sp1 and logs
// 'after' past it.
func newHookedPostgres(t *testing.T, mode string) *hookedPostgres {
	t.Helper()

	env := newComposeProject(t, "pghooks")
	env.upDocument(t, fmt.Sprintf(postgresHooksSpec, env.project, mode))
	binary := newBinaryEnv(t, env)
	db := env.container(t, "db")
	env.exec(t, db, "psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-c", bankSchema)
	env.exec(t, db, "sh", "-c", hookScripts)
	require.Eventually(t, func() bool {
		n, err := strconv.Atoi(env.psqlOrEmpty(db, "select count(*) from ledger"))
		return err == nil && n > 200
	}, time.Minute, 100*time.Millisecond, "the bank transfers")
	entry := env.inspect(t, db)
	created := binary.arcave(t, nil, "--progress", "json", "savepoint", "create", "sp1")
	require.Zero(t, created.exitCode, "%s\n%s", created.stderr, created.stdout)
	held := env.inspect(t, env.container(t, "db"))
	env.waitPostgres(t)
	logBefore := env.psql(t, "select string_agg(phase, ',' order by id) from hook_log")
	env.psql(t, "insert into hook_log (phase) values ('after')")
	return &hookedPostgres{
		hooksEnv:  binary,
		created:   created,
		entry:     entry,
		held:      held,
		logBefore: logBefore,
	}
}

// rollBack rolls back to sp1 and reads the restored postgres.
func (p *hookedPostgres) rollBack(t *testing.T) restoredPostgres {
	t.Helper()

	run := p.arcave(t, nil, "rollback", "sp1")
	require.Zero(t, run.exitCode, "%s", run.stderr)
	p.waitPostgres(t)
	restored := p.container(t, "db")
	return restoredPostgres{
		run:        run,
		logAfter:   p.psql(t, "select string_agg(phase, ',' order by id) from hook_log"),
		checkpoint: p.psql(t, "select lsn::text from hook_log where phase = 'checkpoint'"),
		total:      p.psql(t, "select sum(balance) from accounts"),
		unbalanced: p.psql(t, unbalancedAccounts),
		amcheck: p.tryExec(restored,
			"pg_amcheck", "-U", "postgres", "--install-missing", "--heapallindexed", "postgres"),
		redos: redoStarts.FindAllStringSubmatch(p.containerLogs(t, restored), -1),
	}
}

// assertConsistent checks what every mode promises: the hooks ran in the database on either side
// of the hold, every transfer is whole and every relation checks clean.
func (p *hookedPostgres) assertConsistent(t *testing.T, restored restoredPostgres) {
	t.Helper()

	assert.Equal(t, "pre,checkpoint,post", p.logBefore, "the hooks ran in the database, in order")
	kinds := readHookStream(t, p.created.stdout).kinds
	assert.Contains(t, kinds, progress.Kind("hook.pre-capture"))
	assert.Contains(t, kinds, progress.Kind("hook.post-capture"))
	assert.Equal(t, 2, countKind(kinds, "hook"), "kinds: %v", kinds)
	assert.Equal(t, "pre,checkpoint", restored.logAfter, "pre-capture ran before the hold, post-capture after the copy")
	assert.Equal(t, "10000", restored.total, "no transfer is torn")
	assert.Equal(t, "0", restored.unbalanced, "the ledger explains every balance")
	require.NoError(t, restored.amcheck.err)
	assert.Zero(t, restored.amcheck.exitCode, "pg_amcheck: %s", restored.amcheck.output)
}

// documentedHookSpec is the postgres recipe of docs/capture_modes.md, as compose reads it.
const documentedHookSpec = `
name: %s
services:
  db:
    image: postgres:16
    environment:
      POSTGRES_HOST_AUTH_METHOD: trust
      POSTGRES_USER: postgres
    labels:
      arcave.capture.mode: pause
      arcave.image.capture: "false"
      arcave.hook.pre-capture: sh -c 'psql -U "$$POSTGRES_USER" -c CHECKPOINT'
      arcave.hook.pre-capture.timeout: 2m
    healthcheck:
      test: ["CMD", "pg_isready", "-h", "127.0.0.1", "-U", "postgres"]
      interval: 1s
      retries: 60
    volumes:
      - pgdata:/var/lib/postgresql/data
volumes:
  pgdata: {}
`

// The documented postgres hook, interpolated by compose as the user's file is, checkpoints.
func TestSavepoint_DocumentedPostgresHook(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newComposeProject(t, "pghooks")
	// `docker compose` interpolates the file as it does the user's.
	env.upDocument(t, fmt.Sprintf(documentedHookSpec, env.project))
	binary := newBinaryEnv(t, env)
	env.waitPostgres(t)
	db := env.container(t, "db")
	before, err := strconv.Atoi(env.psql(t, "select checkpoints_req from pg_stat_bgwriter"))
	require.NoError(t, err)

	// Act
	run := binary.arcave(t, nil, "savepoint", "create", "sp1")

	// Assert
	require.Zero(t, run.exitCode, "%s", run.stderr)
	assert.Eventually(t, func() bool {
		after, err := strconv.Atoi(env.psqlOrEmpty(db, "select checkpoints_req from pg_stat_bgwriter"))
		return err == nil && after > before
	}, 30*time.Second, 100*time.Millisecond, "the hook checkpointed; checkpoints_req was %d", before)
}

// psql runs sql in db's first replica and returns its unaligned output.
func (e *composeProject) psql(t *testing.T, sql string) string {
	t.Helper()

	return strings.TrimSpace(e.exec(t, e.container(t, "db"), "psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-tAc", sql))
}

// psqlOrEmpty runs sql in the container, empty when it fails; it never fails the test, so it is
// safe inside Eventually.
func (e *composeProject) psqlOrEmpty(containerID, sql string) string {
	outcome := e.tryExec(containerID, "psql", "-U", "postgres", "-tAc", sql)
	if outcome.err != nil || outcome.exitCode != 0 {
		return ""
	}
	return strings.TrimSpace(outcome.output)
}

// waitPostgres waits for db's postgres to take queries.
func (e *composeProject) waitPostgres(t *testing.T) {
	t.Helper()

	db := e.container(t, "db")
	require.Eventually(t, func() bool {
		return e.psqlOrEmpty(db, "select 1") == "1"
	}, time.Minute, 100*time.Millisecond, "postgres takes queries")
}

// execOutcome is how one exec ended: its stdout and stderr together, its exit code, or why it did
// not run.
type execOutcome struct {
	output   string
	exitCode int
	err      error
}

// tryExec runs one command inside a container and reports how it ended, failing nothing.
func (e *composeProject) tryExec(containerID string, cmd ...string) execOutcome {
	created, err := e.daemon.ExecCreate(e.ctx, containerID, client.ExecCreateOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return execOutcome{err: err}
	}
	attached, err := e.daemon.ExecAttach(e.ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return execOutcome{err: err}
	}
	defer attached.Close()

	var out bytes.Buffer
	if _, err = stdcopy.StdCopy(&out, &out, attached.Reader); err != nil {
		return execOutcome{err: err}
	}
	inspect, err := e.daemon.ExecInspect(e.ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return execOutcome{err: err}
	}
	return execOutcome{
		output:   out.String(),
		exitCode: inspect.ExitCode,
	}
}

// containerLogs is everything the container wrote to stdout and stderr.
func (e *composeProject) containerLogs(t *testing.T, containerID string) string {
	t.Helper()

	logs, err := e.daemon.ContainerLogs(e.ctx, containerID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	require.NoError(t, err)
	defer logs.Close()

	var out bytes.Buffer
	_, err = stdcopy.StdCopy(&out, &out, logs)
	require.NoError(t, err)
	return out.String()
}

// parseLSN is the position a postgres LSN spells as hi/lo in hex.
func parseLSN(t *testing.T, lsn string) uint64 {
	t.Helper()

	hi, lo, ok := strings.Cut(lsn, "/")
	require.True(t, ok, "lsn %q", lsn)
	high, err := strconv.ParseUint(hi, 16, 32)
	require.NoError(t, err, "lsn %q", lsn)
	low, err := strconv.ParseUint(lo, 16, 32)
	require.NoError(t, err, "lsn %q", lsn)
	return high<<32 | low
}

// countKind is how many of kinds are kind.
func countKind(kinds []progress.Kind, kind progress.Kind) int {
	n := 0
	for _, k := range kinds {
		if k == kind {
			n++
		}
	}
	return n
}
