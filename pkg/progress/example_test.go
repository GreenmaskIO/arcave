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

package progress_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/pkg/progress"
	"github.com/greenmaskio/arcave/pkg/progress/reporter"
)

// The kinds a consumer declares.
const (
	kindAlbumPublish  progress.Kind = "album.publish"
	kindAlbumBackup   progress.Kind = "album.backup"
	kindAlbumExport   progress.Kind = "album.export"
	kindAlbumExtras   progress.Kind = "album.extras"
	kindAlbumRollback progress.Kind = "album.rollback"
	kindPhotoResize   progress.Kind = "photo.resize"
	kindCDNUpload     progress.Kind = "cdn.upload"
	kindCDNPurge      progress.Kind = "cdn.purge"
	kindQueuePause    progress.Kind = "queue.pause"
	kindQueueResume   progress.Kind = "queue.resume"
	kindManifestWrite progress.Kind = "manifest.write"
	kindPhoto         progress.Kind = "photo"
	kindFile          progress.Kind = "file"

	resourceWorker progress.ResourceKind = "worker"
)

var errDenied = errors.New("403 forbidden")

type photo struct {
	name string
	size int64
}

// publisher stands for a consumer's service; its clock is the example's, moved by hand.
type publisher struct {
	clock *exampleClock
}

type exampleClock struct {
	now time.Time
}

func (c *exampleClock) Now() time.Time {
	return c.now
}

func (c *exampleClock) advance(d time.Duration) {
	c.now = c.now.Add(d)
}

var (
	summer = []photo{{"beach.jpg", 2_100_000}, {"dunes.jpg", 1_800_000}, {"sunset.jpg", 2_400_000}}
	start  = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
)

// Example_publish runs a workflow to success through the log reporter.
func Example_publish() {
	var (
		clock   = &exampleClock{now: start}
		logger  = newExampleLogger()
		tracker = progress.New(reporter.NewLog(logger), clock.Now)
		p       = publisher{clock: clock}
	)

	err := p.publish(context.Background(), tracker, summer[:2])

	fmt.Println("err:", err)
	// Output:
	// INF album.publish[summer] status=started
	// INF album.publish[summer]/queue.pause status=started
	// DBG album.publish[summer]/queue.pause msg=Draining resource=worker:worker-1 status=in_progress
	// INF album.publish[summer]/queue.pause msg=Paused resource=worker:worker-1 status=in_progress
	// DBG album.publish[summer]/queue.pause msg=Draining resource=worker:worker-2 status=in_progress
	// INF album.publish[summer]/queue.pause msg=Paused resource=worker:worker-2 status=in_progress
	// INF album.publish[summer]/queue.pause elapsed=400ms status=succeeded
	// INF album.publish[summer]/album.export status=started
	// WRN album.publish[summer]/album.export msg="2 raw files skipped" status=warned
	// INF album.publish[summer]/album.export/photo.resize items=0/2 status=started
	// INF album.publish[summer]/album.export/photo.resize/photo[beach.jpg] status=started
	// INF album.publish[summer]/album.export/photo.resize/photo[beach.jpg] bytes=2.1MB/2.1MB elapsed=600ms status=succeeded stored=420KB
	// INF album.publish[summer]/album.export/photo.resize items=1/2 status=in_progress
	// INF album.publish[summer]/album.export/photo.resize/photo[dunes.jpg] status=started
	// INF album.publish[summer]/album.export/photo.resize/photo[dunes.jpg] bytes=1.8MB/1.8MB elapsed=450ms status=succeeded stored=360KB
	// INF album.publish[summer]/album.export/photo.resize items=2/2 status=in_progress
	// INF album.publish[summer]/album.export/photo.resize elapsed=1.05s items=2/2 status=succeeded
	// INF album.publish[summer]/album.export/manifest.write status=started
	// INF album.publish[summer]/album.export/manifest.write elapsed=50ms status=succeeded
	// INF album.publish[summer]/album.export elapsed=1.1s status=succeeded
	// INF album.publish[summer]/cdn.upload items=0/2 status=started
	// INF album.publish[summer]/cdn.upload/file[beach.jpg] status=started
	// INF album.publish[summer]/cdn.upload/file[beach.jpg] bytes=2.1MB/2.1MB elapsed=350ms status=succeeded
	// INF album.publish[summer]/cdn.upload items=1/2 status=in_progress
	// INF album.publish[summer]/cdn.upload/file[dunes.jpg] status=started
	// INF album.publish[summer]/cdn.upload/file[dunes.jpg] bytes=1.8MB/1.8MB elapsed=350ms status=succeeded
	// INF album.publish[summer]/cdn.upload items=2/2 status=in_progress
	// INF album.publish[summer]/cdn.upload elapsed=700ms items=2/2 status=succeeded
	// INF album.publish[summer]/queue.resume status=started
	// INF album.publish[summer]/queue.resume msg=Running resource=worker:worker-1 status=in_progress
	// INF album.publish[summer]/queue.resume elapsed=100ms status=succeeded
	// INF album.publish[summer] elapsed=2.3s status=succeeded
	// err: <nil>
}

// Example_publishFailure runs the same export under another root and fails an upload, through
// the JSON reporter: the error is on the file alone, the rest of the batch is cancelled.
func Example_publishFailure() {
	var (
		clock   = &exampleClock{now: start}
		tracker = progress.New(reporter.NewJSON(os.Stdout), clock.Now)
		p       = publisher{clock: clock}
	)

	err := p.backup(context.Background(), tracker, summer[:1], 1)

	fmt.Println("err:", err)
	// Output:
	// {"node":1,"status":"started","open":{"step":{"kind":"album.backup","type":"workflow","name":"summer"}},"sample":{"elapsed_ns":0},"at":"2026-10-29T12:00:00Z"}
	// {"node":2,"status":"started","open":{"parent":1,"step":{"kind":"album.export","type":"workflow"}},"sample":{"elapsed_ns":0},"at":"2026-10-29T12:00:00Z"}
	// {"node":2,"status":"warned","msg":"2 raw files skipped","sample":{"elapsed_ns":0},"at":"2026-10-29T12:00:00Z"}
	// {"node":3,"status":"started","open":{"parent":2,"step":{"kind":"photo.resize","type":"workflow"},"measures":["items"]},"sample":{"elapsed_ns":0,"items":{"done":0,"total":1}},"at":"2026-10-29T12:00:00Z"}
	// {"node":4,"status":"started","open":{"parent":3,"step":{"kind":"photo","type":"activity","id":"beach.jpg"},"measures":["bytes"]},"sample":{"elapsed_ns":0},"at":"2026-10-29T12:00:00Z"}
	// {"node":4,"status":"in_progress","sample":{"elapsed_ns":150000000,"bytes":{"done":1000000,"total":2100000}},"at":"2026-10-29T12:00:00.15Z"}
	// {"node":4,"status":"in_progress","sample":{"elapsed_ns":450000000,"bytes":{"done":2100000,"total":2100000}},"at":"2026-10-29T12:00:00.45Z"}
	// {"node":4,"status":"succeeded","sample":{"elapsed_ns":600000000,"bytes":{"done":2100000,"total":2100000,"stored":420000}},"at":"2026-10-29T12:00:00.6Z"}
	// {"node":3,"status":"in_progress","sample":{"elapsed_ns":600000000,"items":{"done":1,"total":1}},"at":"2026-10-29T12:00:00.6Z"}
	// {"node":3,"status":"succeeded","sample":{"elapsed_ns":600000000,"items":{"done":1,"total":1}},"at":"2026-10-29T12:00:00.6Z"}
	// {"node":5,"status":"started","open":{"parent":2,"step":{"kind":"manifest.write","type":"activity"}},"sample":{"elapsed_ns":0},"at":"2026-10-29T12:00:00.6Z"}
	// {"node":5,"status":"succeeded","sample":{"elapsed_ns":50000000},"at":"2026-10-29T12:00:00.65Z"}
	// {"node":2,"status":"succeeded","sample":{"elapsed_ns":650000000},"at":"2026-10-29T12:00:00.65Z"}
	// {"node":6,"status":"started","open":{"parent":1,"step":{"kind":"cdn.upload","type":"workflow"},"measures":["items"]},"sample":{"elapsed_ns":0,"items":{"done":0,"total":4}},"at":"2026-10-29T12:00:00.65Z"}
	// {"node":7,"status":"started","open":{"parent":6,"step":{"kind":"file","type":"activity","id":"beach.jpg"},"measures":["bytes"]},"sample":{"elapsed_ns":0},"at":"2026-10-29T12:00:00.65Z"}
	// {"node":7,"status":"in_progress","sample":{"elapsed_ns":0,"bytes":{"done":1050000}},"at":"2026-10-29T12:00:00.65Z"}
	// {"node":7,"status":"in_progress","sample":{"elapsed_ns":250000000,"bytes":{"done":1575000,"total":2100000}},"at":"2026-10-29T12:00:00.9Z"}
	// {"node":7,"status":"succeeded","sample":{"elapsed_ns":350000000,"bytes":{"done":2100000,"total":2100000}},"at":"2026-10-29T12:00:01Z"}
	// {"node":6,"status":"in_progress","sample":{"elapsed_ns":350000000,"items":{"done":1,"total":4}},"at":"2026-10-29T12:00:01Z"}
	// {"node":8,"status":"started","open":{"parent":6,"step":{"kind":"file","type":"activity","id":"raw.dng"},"measures":["bytes"]},"sample":{"elapsed_ns":0},"at":"2026-10-29T12:00:01Z"}
	// {"node":8,"status":"in_progress","sample":{"elapsed_ns":0,"bytes":{"done":450000}},"at":"2026-10-29T12:00:01Z"}
	// {"node":8,"status":"failed","msg":"unable to upload","error":"403 forbidden","sample":{"elapsed_ns":250000000,"bytes":{"done":450000}},"at":"2026-10-29T12:00:01.25Z"}
	// {"node":6,"status":"in_progress","sample":{"elapsed_ns":600000000,"items":{"done":1,"failed":1,"total":4}},"at":"2026-10-29T12:00:01.25Z"}
	// {"node":9,"status":"started","open":{"parent":6,"step":{"kind":"file","type":"activity","id":"cover.jpg"},"measures":["bytes"]},"sample":{"elapsed_ns":0},"at":"2026-10-29T12:00:01.25Z"}
	// {"node":9,"status":"cancelled","sample":{"elapsed_ns":0},"at":"2026-10-29T12:00:01.25Z"}
	// {"node":6,"status":"in_progress","sample":{"elapsed_ns":600000000,"items":{"done":1,"failed":1,"cancelled":1,"total":4}},"at":"2026-10-29T12:00:01.25Z"}
	// {"node":10,"status":"started","open":{"parent":6,"step":{"kind":"file","type":"activity","id":"map.png"},"measures":["bytes"]},"sample":{"elapsed_ns":0},"at":"2026-10-29T12:00:01.25Z"}
	// {"node":10,"status":"cancelled","sample":{"elapsed_ns":0},"at":"2026-10-29T12:00:01.25Z"}
	// {"node":6,"status":"in_progress","sample":{"elapsed_ns":600000000,"items":{"done":1,"failed":1,"cancelled":2,"total":4}},"at":"2026-10-29T12:00:01.25Z"}
	// {"node":6,"status":"failed","sample":{"elapsed_ns":600000000,"items":{"done":1,"failed":1,"cancelled":2,"total":4}},"at":"2026-10-29T12:00:01.25Z"}
	// {"node":11,"status":"started","open":{"parent":1,"step":{"kind":"album.rollback","type":"workflow"}},"sample":{"elapsed_ns":0},"at":"2026-10-29T12:00:01.25Z"}
	// {"node":12,"status":"started","open":{"parent":11,"step":{"kind":"cdn.purge","type":"workflow"},"measures":["items"]},"sample":{"elapsed_ns":0,"items":{"done":0,"total":1}},"at":"2026-10-29T12:00:01.25Z"}
	// {"node":13,"status":"started","open":{"parent":12,"step":{"kind":"file","type":"activity","id":"beach.jpg"}},"sample":{"elapsed_ns":0},"at":"2026-10-29T12:00:01.25Z"}
	// {"node":13,"status":"warned","msg":"purge failed, the file stays on the CDN: edge timeout","sample":{"elapsed_ns":50000000},"at":"2026-10-29T12:00:01.3Z"}
	// {"node":13,"status":"succeeded","sample":{"elapsed_ns":50000000},"at":"2026-10-29T12:00:01.3Z"}
	// {"node":12,"status":"in_progress","sample":{"elapsed_ns":50000000,"items":{"done":1,"total":1}},"at":"2026-10-29T12:00:01.3Z"}
	// {"node":12,"status":"succeeded","sample":{"elapsed_ns":50000000,"items":{"done":1,"total":1}},"at":"2026-10-29T12:00:01.3Z"}
	// {"node":11,"status":"succeeded","sample":{"elapsed_ns":50000000},"at":"2026-10-29T12:00:01.3Z"}
	// {"node":1,"status":"failed","sample":{"elapsed_ns":1300000000},"at":"2026-10-29T12:00:01.3Z"}
	// err: 403 forbidden
}

// Example_lazy shows a workflow that reports nothing until it has something to report.
func Example_lazy() {
	var (
		clock   = &exampleClock{now: start}
		tracker = progress.New(reporter.NewLog(newExampleLogger()), clock.Now)
	)

	ctx, root := tracker.Begin(context.Background(), progress.Step{Kind: kindAlbumPublish, Name: "summer"})
	_, extras := progress.Begin(ctx, progress.Step{Kind: kindAlbumExtras})
	extras.End(nil)
	fmt.Println("an empty workflow that succeeded reported nothing")
	clock.advance(time.Second)
	_, rollback := progress.Begin(ctx, progress.Step{Kind: kindAlbumRollback})
	rollback.End(errors.New("queue unreachable"))
	root.End(errors.New("rollback: queue unreachable"))

	// Output:
	// an empty workflow that succeeded reported nothing
	// INF album.publish[summer] status=started
	// INF album.publish[summer]/album.rollback status=started
	// ERR album.publish[summer]/album.rollback error="queue unreachable" elapsed=0s status=failed
	// ERR album.publish[summer] elapsed=1s status=failed
}

func (p publisher) publish(ctx context.Context, tracker *progress.Tracker, photos []photo) (err error) {
	ctx, root := tracker.Begin(ctx, progress.Step{Kind: kindAlbumPublish, Name: "summer"})
	defer func() { root.End(err) }()

	if err = p.pauseQueue(ctx); err != nil {
		return err
	}
	if err = p.exportAlbum(ctx, photos); err != nil {
		return err
	}
	if _, err = p.uploadFiles(ctx, photos, -1); err != nil {
		return err
	}
	return p.resumeQueue(ctx)
}

func (p publisher) backup(ctx context.Context, tracker *progress.Tracker, photos []photo, failAt int) (err error) {
	ctx, root := tracker.Begin(ctx, progress.Step{Kind: kindAlbumBackup, Name: "summer"})
	defer func() { root.End(err) }()

	if err = p.exportAlbum(ctx, photos); err != nil {
		return err
	}
	uploaded, err := p.uploadFiles(ctx, append(photos, photo{"raw.dng", 900_000}, photo{"cover.jpg", 300_000}, photo{"map.png", 100_000}), failAt)
	if err != nil {
		if rollbackErr := p.rollbackAlbum(ctx, uploaded); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return nil
}

// pauseQueue is a summary activity: one call handles every worker, each a resource.
func (p publisher) pauseQueue(ctx context.Context) error {
	_, node := progress.Run(ctx, progress.Step{Kind: kindQueuePause}, progress.MeasureResources)
	for _, worker := range []string{"worker-1", "worker-2"} {
		resource := progress.Resource{Kind: resourceWorker, ID: worker}
		node.UpdateResource(resource, "Draining")
		p.clock.advance(200 * time.Millisecond)
		node.SettleResource(resource, "Paused")
	}
	node.Done(progress.Sample{})
	return nil
}

func (p publisher) resumeQueue(ctx context.Context) error {
	_, node := progress.Run(ctx, progress.Step{Kind: kindQueueResume}, progress.MeasureResources)
	p.clock.advance(100 * time.Millisecond)
	node.SettleResource(progress.Resource{Kind: resourceWorker, ID: "worker-1"}, "Running")
	node.Done(progress.Sample{})
	return nil
}

// exportAlbum is a shared workflow: it does not know which root it runs under.
func (p publisher) exportAlbum(ctx context.Context, photos []photo) (err error) {
	ctx, node := progress.Begin(ctx, progress.Step{Kind: kindAlbumExport})
	defer func() { node.End(err) }()

	node.Warn("2 raw files skipped")
	batchCtx, batch := progress.Batch(ctx, progress.Step{Kind: kindPhotoResize}, len(photos))
	for _, item := range photos {
		if err = p.resizePhoto(batchCtx, item); err != nil {
			batch.End(err)
			return err
		}
	}
	batch.End(nil)

	_, extras := progress.Begin(ctx, progress.Step{Kind: kindAlbumExtras})
	extras.End(nil)

	_, manifest := progress.Run(ctx, progress.Step{Kind: kindManifestWrite})
	p.clock.advance(50 * time.Millisecond)
	manifest.Done(progress.Sample{})
	return nil
}

func (p publisher) resizePhoto(ctx context.Context, item photo) (err error) {
	ctx, node := progress.Run(ctx, progress.Step{Kind: kindPhoto, ID: item.name}, progress.MeasureBytes)
	defer func() { node.End(err) }()

	resized, err := p.resize(ctx, bytes.NewReader(make([]byte, item.size)), item.size)
	if err != nil {
		return err
	}
	node.Done(progress.Sample{Bytes: &progress.Bytes{Done: item.size, Total: item.size, Stored: resized}})
	return nil
}

// resize is a source deep in the call: it knows ctx and nothing of the tree.
func (p publisher) resize(ctx context.Context, src io.Reader, size int64) (int64, error) {
	src = progress.NewReader(ctx, src, size)
	buf := make([]byte, 1_000_000)
	for {
		p.clock.advance(150 * time.Millisecond)
		if _, err := src.Read(buf); errors.Is(err, io.EOF) {
			return size / 5, nil
		} else if err != nil {
			return 0, err
		}
	}
}

// uploadFiles is a batch whose objects report what the CDN counts itself; the first failure
// cancels the rest, as an errgroup would.
func (p publisher) uploadFiles(ctx context.Context, photos []photo, failAt int) (uploaded []photo, err error) {
	ctx, batch := progress.Batch(ctx, progress.Step{Kind: kindCDNUpload}, len(photos))
	defer func() { batch.End(err) }()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i, item := range photos {
		uploadErr := p.uploadFile(ctx, item, i == failAt)
		if uploadErr == nil {
			uploaded = append(uploaded, item)
			continue
		}
		if err == nil {
			err = uploadErr
			cancel()
		}
	}
	return uploaded, err
}

func (p publisher) uploadFile(ctx context.Context, item photo, deny bool) (err error) {
	_, node := progress.Run(ctx, progress.Step{Kind: kindFile, ID: item.name}, progress.MeasureBytes)
	defer func() { node.End(err) }()

	if err = ctx.Err(); err != nil {
		return err
	}
	// The CDN counts the bytes itself and names the total only once it has the whole file.
	sampler := progress.NewSampler(node, progress.Sample{Bytes: &progress.Bytes{}})
	sampler.Update(func(sample *progress.Sample) {
		sample.Bytes.Done = item.size / 2
	})
	p.clock.advance(250 * time.Millisecond)
	if deny {
		node.Fail("unable to upload", errDenied)
		return errDenied
	}
	sampler.Update(func(sample *progress.Sample) {
		sample.Bytes.Done = item.size * 3 / 4
		sample.Bytes.Total = item.size
	})
	p.clock.advance(100 * time.Millisecond)
	sampler.Update(func(sample *progress.Sample) {
		sample.Bytes.Done = item.size * 7 / 8
	})
	node.Done(progress.Sample{Bytes: &progress.Bytes{Done: item.size, Total: item.size}})
	return nil
}

// rollbackAlbum purges what was uploaded; a purge that fails only warns.
func (p publisher) rollbackAlbum(ctx context.Context, uploaded []photo) (err error) {
	ctx, node := progress.Begin(ctx, progress.Step{Kind: kindAlbumRollback})
	defer func() { node.End(err) }()

	ctx, batch := progress.Batch(ctx, progress.Step{Kind: kindCDNPurge}, len(uploaded))
	defer func() { batch.End(err) }()
	for _, item := range uploaded {
		_, purge := progress.Run(ctx, progress.Step{Kind: kindFile, ID: item.name})
		p.clock.advance(50 * time.Millisecond)
		purge.Warn("purge failed, the file stays on the CDN: edge timeout")
		purge.Done(progress.Sample{})
	}
	return nil
}

func newExampleLogger() zerolog.Logger {
	return zerolog.New(zerolog.ConsoleWriter{
		Out:          os.Stdout,
		NoColor:      true,
		PartsExclude: []string{zerolog.TimestampFieldName},
	})
}
