# progress

A workflow's progress as a stream of events over a tree of nodes. The tree follows the code's
own composition: a workflow opens its nodes under whatever node the context carries, and never
knows who called it. The package declares no kinds and knows nothing of arcave; the examples
publish a photo album (`example_test.go`, and the runnable `examples/`). It owns its types
(`event.go`) and imports only the standard library and zerolog, so it can leave the repository
whole; the terminal reporter's Bubble Tea and Lip Gloss stay in `reporter/tty`, and

```bash
go list -deps ./pkg/progress ./pkg/progress/reporter | grep charm
```

stays empty.

## Nodes

| Constructor | Type | Opens | Measures |
|---|---|---|---|
| `Tracker.Begin` | workflow, the root | lazily | none |
| `Begin` on `WithTracker(ctx, t)` | workflow, `t`'s root, when `ctx` carries no node | lazily | none |
| `Begin` | workflow | lazily — on its first child or `Warn` | none |
| `Batch` | workflow over objects of one kind | at once, with `Items.Total` | `items`, counted by the tracker |
| `Run` | activity — one action, no children | at once | what the caller declares |

`WithTracker` lets a workflow open its own root without knowing whether it is one: the same
`Begin` opens the tracker's root at the top of a run, and a child when another workflow calls it.

Every constructor returns a context carrying the new node. Code deeper down that holds only the
context reaches its activity through `Current(ctx)`. `WithNode(ctx, node)` puts a node back on a context
derived from an activity's — a lock's, say, whose context the rest of the workflow runs on. A constructor under an activity, or under a node
that has ended, returns a mute node and logs a warning; so does every constructor when the context carries no tracker,
and under `Discard()`. Every method of a mute node is a no-op, so a consumer's tests change
nothing.

A lazy workflow that ends without a child reports nothing: what never ran is never mentioned.
`Update` and `Progress` keep it unopened; only a child, `Warn` or a failure opens it. A batch
still counts such a child among its done.
There is no plan, no `pending` and no `skipped`; a unit left out is a warning on its parent.
`Items.Total` of a batch is a fact, the count it will really process once filtered, 0 if unknown.

## Kinds

A `progress.Kind` is a string the consumer declares. By convention an action is
`<namespace>.<action>` (`queue.pause`, `photo.resize`) and an object a bare noun (`photo`,
`file`). A batch's children are objects, whose kind is not derived from the batch's: `cdn.upload`
loops over `file`. The convention is for readers; no renderer depends on it.

## The stream

```
activity (Run):   started+Open ──► (in_progress | warned)* ──► succeeded | failed | cancelled
batch (Batch):    started+Open ──► in_progress per finished child ──► succeeded | failed | cancelled
workflow (Begin): [lazy] ──first child / Warn──► started+Open ──► (warned)* ──► succeeded | failed | cancelled
```

- Nodes are numbered from 1 in the order they open; a lazy workflow opens its unopened
  ancestors top down first, so a parent's `started` always precedes its child's.
- `started` carries `Open` — the parent, the step and the declared `Measures` — once. Every
  later event names the node by number only: a renderer reads the stream from its start and
  builds a node's widget once, as compose's and OpenTelemetry's do.
- Every event is a full snapshot: the current `msg`, the last sample, a fresh `Elapsed` counted
  from the constructor. A caller reports only what changed: `Done(sample)` and `Progress(sample)`
  replace the fields they set and keep the rest; `Fail` keeps the last sample.
- A node ends once; every call after is ignored.

## Samples

`Elapsed` (always, `elapsed_ns` in JSON), `Bytes{Done, Total, Stored, Reused}` and
`Items{Done, Failed, Cancelled, Total}`, accumulated rather than deltas. `Items.Done` counts
successes only — `3/5 failed=1` is three done and one failed of five. A `Total` of 0 is unknown
and left out of the JSON: a renderer shows `Done` alone, with no bar; a `Total` may appear later
in the same stream, and a `Done` past it is shown without one. Anything not a count goes in `msg`.

A node reports only the measures it declared; the rest is dropped with a warning. A batch counts
its children's outcomes itself and takes no `Items` from its caller; it never sums their bytes.

## Failure and cancellation

`Fail(msg, err)`, or `End(err)` for a non-nil `err`, ends a node `failed` — or `cancelled` when
`errors.Is(err, context.Canceled)`, or when the context the node was opened on is cancelled and
`err` is its `context.Cause`: the siblings an errgroup cancels after the first failure are
counted `Cancelled`, not `Failed`, and carry no error. A deadline is a failure.

A node opens on the context **outside** the cancellation it reports. A workflow that cancels
its own work with a cause — a lost lock, a missed heartbeat — opens its node before deriving
that context: its children end `cancelled` and the node itself `failed`, carrying the cause
once. Opened under that context, it would end `cancelled` too, and the cause would not be in
the stream at all. Likewise a `Batch` opens before `errgroup.WithContext`, and its children
open on the group's context:

```go
ctx, batch := progress.Batch(ctx, progress.Step{Kind: kindCDNUpload}, len(files))
defer func() { batch.End(err) }()
eg, egCtx := errgroup.WithContext(ctx)
for _, f := range files {
	eg.Go(func() error {
		_, node := progress.Run(egCtx, progress.Step{Kind: kindFile, ID: f.name}, progress.MeasureBytes)
		...
	})
}
```

The error is reported where it arose: always on an activity, on a workflow only when none of
its descendants failed — an error raised by the workflow's own code. The ancestors of a failed
node end `failed` with no error, so the cause is in the stream exactly once.

A best-effort step whose failure does not fail the operation calls `Warn` and then `Done`.
`WarnInResult` is `Warn` for a warning the operation's result repeats: its event carries
`"in_result": true`, a log or a JSON stream reports it as any warning, and a renderer printing
the result beside the tree may leave it out.

`End` under `defer` must close over a named error, or it reports success:

```go
func (p publisher) exportAlbum(ctx context.Context, photos []photo) (err error) {
	ctx, node := progress.Begin(ctx, progress.Step{Kind: kindAlbumExport})
	defer func() { node.End(err) }() // not defer node.End(err)
	...
}
```

## Activities over resources

An activity handles its resources one of two ways. When a batch runs one object per resource,
the resource's key is the object's `Step.ID`. When one call handles the whole set — a queue
pausing every worker — the activity declares `resources` and reports each through
`UpdateResource` while one is under way, `SettleResource` once it reaches its outcome — the
event's `settled` — and `WarnResource` with a `progress.Resource`; the resources are not nodes.
The `Log` reporter writes a resource under way at debug and a settled one at info.

## Sources deep in the call

A source that counts as it goes reports through a `Sampler`: its topic is the node to report on,
its template the sample's shape and the values known upfront, such as a `Total`. `Update(fn)`
changes the accumulated sample — adding as a stream flows, or replacing it with numbers the
source counts itself — and sends it at most once per the tracker's interval (200ms on its
clock); the first update goes at once, and the last is never flushed: the node's `Done` carries
the total. Throttling in the source keeps the tracker's lock off every 32KB read of parallel
streams. A sampler whose topic is mute, or does not declare the template's measures, is mute.

`NewReader(ctx, r, total)` and `NewWriter(ctx, w, total)` count a stream's bytes on
`Current(ctx)` through a sampler, and return `r` or `w` itself when that node reports no bytes.
Pass a `total` only when it is exact, else 0: an estimate draws a bar stuck at 100% or one that
turns into a counter midway, which reads as a fault.

```go
src = progress.NewReader(ctx, src, size) // the source knows ctx, not the tree

sampler := progress.NewSampler(node, progress.Sample{Bytes: &progress.Bytes{}})
sampler.Update(func(sample *progress.Sample) {
	sample.Bytes.Done = status.BytesDone
	sample.Bytes.Total = status.TotalBytes // 0 until the source has counted
})
```

## Reporters

`Reporter` is the one seam: `Report(progress.Event)`, called under the tracker's lock.
`reporter.NewJSON` writes the stream as is, one event per line, and stops at a failed write,
which `Err` returns; a consumer rebuilds the tree by `open.parent`. `reporter.NewLog` writes a record per event worth a line, named by the node's
path, and skips a sample tick that changes nothing but bytes:

```
INF album.publish[summer]/album.export status=started
WRN album.publish[summer]/album.export msg="2 raw files skipped" status=warned
INF album.publish[summer]/album.export/photo.resize items=0/2 status=started
INF album.publish[summer]/album.export/photo.resize/photo[beach.jpg] status=started
INF album.publish[summer]/album.export/photo.resize/photo[beach.jpg] bytes=2.1MB/2.1MB elapsed=600ms status=succeeded stored=420KB
INF album.publish[summer]/album.export/photo.resize items=1/2 status=in_progress
ERR album.backup[summer]/cdn.upload/file[raw.dng] error="403 forbidden" bytes=450KB elapsed=250ms msg="unable to upload" status=failed
WRN album.backup[summer]/cdn.upload/file[cover.jpg] elapsed=0s status=cancelled
```

```json
{"node":8,"status":"started","open":{"parent":6,"step":{"kind":"file","type":"activity","id":"raw.dng"},"measures":["bytes"]},"sample":{"elapsed_ns":0},"at":"2026-10-29T12:00:01Z"}
{"node":8,"status":"in_progress","sample":{"elapsed_ns":0,"bytes":{"done":450000}},"at":"2026-10-29T12:00:01Z"}
{"node":8,"status":"failed","msg":"unable to upload","error":"403 forbidden","sample":{"elapsed_ns":250000000,"bytes":{"done":450000}},"at":"2026-10-29T12:00:01.25Z"}
```

### The terminal

`tty.New(out)` draws the stream on a terminal as a tree redrawn in place, through Bubble Tea:

```
⠦ album.publish summer                                               3.6s
├── ✔ queue.pause                                                    0.3s
├── ✔ album.export                                                   2.1s
│     ! 2 raw files skipped
│   ├── ✔ photo.resize  8/8                                          1.9s
│   └── ✔ manifest.write                                             0.1s
└── ⠦ cdn.upload  5/8                                                1.3s
    └── ⠦ file cliffs.jpg  ████████████░░░░░░░░ 1.8MB/2.7MB 7.0MB/s  0.3s
```

- A line is `<icon> <kind> <name or id>  <widget>  <msg>`, the time right-aligned in one column
  just past the widest line, which a live frame never moves left: a spinner while running, `✔`,
  `✘` or `⊘` once ended. `bytes` is a bar, the count and the average speed — the count alone
  while the total is unknown or exceeded, or once every byte is in, `stored`/`reused` once ended;
  `items` is `done/total`, `✘failed`, `⊘cancelled`; `resources` is a line per resource under the
  node. A warning is a `!` line under its node, an error an `error:` line. A node cancelled before
  it took any time shows none. What does not fit the width gives way: the label, the message,
  the bar.
- Each root is its own tree. A success with no warning or failure below it is one line. Under a
  batch, running or ended, only what has something to say is shown — running, failed, warned, or
  cancelled after it began; a quiet success and a cancellation that stopped nothing under way are
  left to the batch's count. A quiet success stays open for 1.5s after it ends, so a quick
  step's resource lines are seen; under a batch it goes at once. A frame taller than the terminal
  folds the earliest finished children into `… N more finished`; running and failed nodes always
  show.
- `Stop(StopOptions)` clears the frame and writes the final tree into the scrollback, collapsed by
  the same rules but neither cut nor folded, a root always open, five warnings at most per node
  and the rest counted, `MoreHint` after the count. `Essential`, for a caller printing a result
  beside the tree, writes the roots and each root's children as a line with its time, and below
  them only the paths to what the result does not say: failures, cancellations, warned resources
  and warnings not marked `in_result`. `OmitRootError` leaves a root's `error:` line to the caller,
  which writes the error itself:

```
✘ album.backup summer                                  2.5s
├── ✔ album.export                                     1.2s
│     ! 2 raw files skipped
│   ├── ✔ photo.resize  3/3                            1.0s
│   └── ✔ manifest.write                               0.1s
├── ✘ cdn.upload  3/6 ✘1 ⊘2                            1.0s
│   └── ✘ file raw.dng  450KB/900KB  unable to upload  0.1s
│         error: 403 forbidden
└── ✔ album.rollback                                   0.3s
    └── ✔ cdn.purge  3/3                               0.3s
        └── ✔ file beach.jpg                           0.1s
              ! purge failed, the file stays on the CDN: edge timeout
```

`Report` and `LogWriter` only change the reporter's state under its own lock; the program reads
it on a 100ms tick, so the tracker never waits on the terminal. Log lines go through
`LogWriter()` — an `io.Writer` of whole lines, filtered by the logger's level — and print above
the tree. The program reads no input and handles no signal: Ctrl-C stays the caller's, to cancel
its context. `Start` begins drawing; `Stop` must follow on every path, a panic included:

```go
progressReporter := tty.New(os.Stderr)
logger := zerolog.New(zerolog.ConsoleWriter{Out: progressReporter.LogWriter()}).Level(zerolog.WarnLevel)
if err := progressReporter.Start(); err != nil {
	return err
}
defer progressReporter.Stop(tty.StopOptions{})
tracker := progress.New(progressReporter, time.Now)
```

`go run ./pkg/progress/examples -scenario all` runs four workflows through it — success, failure
with cancellation and rollback, four uploads at once, a source with no total — or through
`-reporter log|json`; `-speed` scales them.
