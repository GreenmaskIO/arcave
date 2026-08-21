# Writing tests

Conventions for **new and modified** tests. Some older tests still use bare `t.Errorf`/`t.Fatalf`; do not copy that style — when you touch such a test, migrate it toward what is below. The short form is in [`CLAUDE.md`](../CLAUDE.md#writing-tests).

Verified against commit `360ad7e` (2026-08-13). Every example names something that exists in this repository.

## 1. Table-driven wherever possible

If a behaviour has more than one meaningful input/output pair, express it as a table: a slice of anonymous structs, one `t.Run` per case with a descriptive lowercase `name`.

`TestInitService_Init` in `internal/provider/docker/internal/core/init_test.go` is the reference. Note its `verify func(t *testing.T, saved config.Config)` field — that is the pattern for cases needing bespoke assertions rather than a shared `want`:

```go
tests := []struct {
	name    string
	cfg     config.Config
	exists  bool  // does .arcave/config.yaml already exist?
	wantErr string
	verify  func(t *testing.T, saved config.Config)
}{
	{
		name: "writes the config",
		cfg:  config.Config{Project: config.Project{Name: "demo"}},
		verify: func(t *testing.T, saved config.Config) {
			assert.Equal(t, config.DefaultControlPlaneAddress, saved.ControlPlane.Address)
		},
	},
	{
		name:    "an initialized project is an error",
		cfg:     config.Config{Project: config.Project{Name: "demo"}},
		exists:  true,
		wantErr: "project already initialized",
	},
}
```

Case names become sub-test ids, so keep them unique. A single-behaviour test with one input does not need a table — don't force it.

## 2. testify: assert vs require

**`require`** when a failure must stop the test because everything after depends on it — setup, error checks, length checks before indexing. **`assert`** for the actual value checks, so each is reported independently and the test collects every mismatch.

```go
records, err := repo.List(ctx)
require.NoError(t, err)            // no point continuing
require.Len(t, records, 1)         // guards records[0] below

assert.Equal(t, "before-migration", records[0].Name)
assert.True(t, records[0].CreatedAt.Equal(want), "created_at = %v", records[0].CreatedAt)
```

Rule of thumb: **`require` for preconditions, `assert` for the things under test.** Compare times with `.Equal()`, never `==`; floats with `InDelta`, never `Equal`; cross-int-type values with `EqualValues`. Never hand-roll `if got != want { t.Errorf(...) }` in new code.

## 3. Mocks are strict

Mock collaborators with `github.com/stretchr/testify/mock`; `internal/provider/docker/internal/core/mocks_test.go` holds the doubles for that package's seams, and new ones follow its shape.

- **`mock.Anything` is allowed only for the `ctx` argument.** A stable argument is matched exactly: `[]string{"docker-compose.yaml"}`, `60*time.Second`, a full request struct.
- **An unstable argument gets `mock.MatchedBy`** carrying the comparison — never widened to `mock.Anything`. An id off the clock, a record carrying `CreatedAt`:

  ```go
  removeSafety := safetySnapshotBackend.On("Remove", mock.Anything,
      mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
          return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
      })).Return(nil).Once()
  ```

- **Every mock with expectations closes its test with its own `AssertExpectations(t)`** — one line per mock, no `AssertExpectationsForObjects`.
- **A negative assertion is an empty mock plus `AssertExpectations(t)`**, not `AssertNotCalled`: a mock with no expectation fails loudly on any call, while `AssertNotCalled` passes when you name the wrong method.
- **Call order is pinned in the expectations themselves.** Declare the `*Call`s in workflow order and chain them with `mock.InOrder(...)`; fan-in via `.NotBefore(a, b)`. `NotBefore` requires the referenced expectation *fully satisfied*, which with `.Times(N)` is barrier semantics. **Never hand-roll a call journal and read it back afterwards.** A method called twice gets two `.Once()` expectations, so each firing has its own place in the chain.
- Expectations a case may legitimately skip are marked `.Maybe()`. Assert on captured arguments through a helper on the double, not by reaching into `mock.Calls`.
- Prefer a real `httptest.Server` when faking an HTTP boundary — `test/resticrest` does this against the real protocol. Use `mock.Mock` for internal Go interfaces.

`internal/provider/docker/internal/core/rollback_test.go` is the worked example: the whole rollback chain — stop → capture the live state → commit its record → restore → start → uncommit and drop — is pinned by one `mock.InOrder`, because every neighbouring pair compiles when swapped.

## 4. AAA structure

Every test body runs Arrange → Act → Assert, in that order, separated by a blank line and a comment when it aids scanning. Keep them distinct: no assertions mixed into arrange, no new arrange after the act.

`TestEngine_WriteVolume_AFailedWipeWritesNothing` in `internal/provider/docker/internal/client/docker/engine_test.go`:

```go
// Arrange: no CopyToContainer expectation, so an extract fails the run.
api := &mockAPI{}
expectImagePresent(api, scratchImage)
expectContainerRun(api, "wipe-1", "find").exit(1)
e := &Engine{api: api}

// Act
err := e.WriteVolume(context.Background(),
	model.MountSource{Type: model.MountSourceVolume, Source: "demo_pgdata"},
	strings.NewReader("payload"))

// Assert
require.ErrorContains(t, err, "clear volume demo_pgdata before restore")
api.AssertExpectations(t)
```

In a table-driven test the Arrange lives partly in the row and partly in the `t.Run` body; Act and Assert live in the closure, in the same order.

## 5. Naming

```
Test{ObjectName}_{methodName}[_{caseName}]
```

- `{ObjectName}` — the type under test (`Service`, `Engine`, `InitService`); omit it for a package-level function.
- `{methodName}` — the method or function under test, verbatim, keeping its original casing.
- `_{caseName}` — **only when the test is not table-driven** and scenarios live in separate functions. A table-driven test carries its cases in the table and stops at the method name.

```go
func TestService_CreateSavepoint(t *testing.T)                    // table-driven, no suffix
func TestEngine_WriteVolume_AFailedWipeWritesNothing(t *testing.T) // separate scenario
func TestRunOneShotContainer_StdioLargePayload(t *testing.T)       // package-level function
```

## 6. The rest

- Mark helpers with `t.Helper()`; use `t.Setenv` rather than `os.Setenv`; silence loggers with `zerolog.Nop()`. Tests needing a live daemon live under `test/` and guard themselves; a green run there also proves the container mechanics, so keep the byte-for-byte assertions in that layer.
- **No sleeps for synchronisation.** Drive time explicitly with fixed `time.Date(...)` values. A test that needs a real clock reads the id back from what the run returned or recorded.
- Some invariants compile clean when wrong and are pinned only by a test. Those tests are load-bearing — `CLAUDE.md`'s storage-invariants section names them, and `TestRunOneShotContainer_StdioLargePayload` is the one that fails by **timing out** rather than by asserting. Do not "fix" such a test by relaxing it.
