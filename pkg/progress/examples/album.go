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

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/pkg/progress"
)

// The kinds the example declares, a copy of example_test.go's publisher's.
const (
	kindAlbumPublish  progress.Kind = "album.publish"
	kindAlbumBackup   progress.Kind = "album.backup"
	kindAlbumImport   progress.Kind = "album.import"
	kindAlbumExport   progress.Kind = "album.export"
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

// readBuffer is how much a slow source hands over per read.
const readBuffer = 32 << 10

var errDenied = errors.New("403 forbidden")

type photo struct {
	name string
	size int64
	// took is how long the photo takes to read at speed 1.
	took time.Duration
}

var (
	summer = []photo{
		{name: "beach.jpg", size: 2_100_000, took: 900 * time.Millisecond},
		{name: "dunes.jpg", size: 1_800_000, took: 700 * time.Millisecond},
		{name: "sunset.jpg", size: 2_400_000, took: time.Second},
		{name: "harbor.jpg", size: 1_500_000, took: 600 * time.Millisecond},
		{name: "market.jpg", size: 2_700_000, took: 1100 * time.Millisecond},
		{name: "cliffs.jpg", size: 1_900_000, took: 800 * time.Millisecond},
		{name: "lagoon.jpg", size: 2_200_000, took: 900 * time.Millisecond},
		{name: "village.jpg", size: 1_600_000, took: 700 * time.Millisecond},
	}
	extras = []photo{
		{name: "raw.dng", size: 900_000, took: 400 * time.Millisecond},
		{name: "cover.jpg", size: 300_000, took: 300 * time.Millisecond},
		{name: "map.png", size: 100_000, took: 200 * time.Millisecond},
	}
)

// clock is the wall clock with every sleep divided by speed.
type clock struct {
	speed float64
}

func (c clock) Now() time.Time {
	return time.Now()
}

// Sleep waits d at speed 1, or until ctx is done.
func (c clock) Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(time.Duration(float64(d) / c.speed))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

// publisher stands for a consumer's service.
type publisher struct {
	clock clock
}

func (p publisher) publish(ctx context.Context, tracker *progress.Tracker) (err error) {
	ctx, root := tracker.Begin(ctx, progress.Step{Kind: kindAlbumPublish, Name: "summer"})
	defer func() { root.End(err) }()

	if err = p.pauseQueue(ctx); err != nil {
		return err
	}
	if err = p.exportAlbum(ctx, summer); err != nil {
		return err
	}
	if _, err = p.uploadFiles(ctx, summer, -1); err != nil {
		return err
	}
	return p.resumeQueue(ctx)
}

func (p publisher) backup(ctx context.Context, tracker *progress.Tracker) (err error) {
	ctx, root := tracker.Begin(ctx, progress.Step{Kind: kindAlbumBackup, Name: "summer"})
	defer func() { root.End(err) }()

	photos := summer[:3]
	if err = p.exportAlbum(ctx, photos); err != nil {
		return err
	}
	uploaded, err := p.uploadFiles(ctx, append(slices.Clip(photos), extras...), len(photos))
	if err != nil {
		if rollbackErr := p.rollbackAlbum(ctx, uploaded); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return nil
}

// parallel uploads twelve files four at a time, as an errgroup over volumes would.
func (p publisher) parallel(ctx context.Context, tracker *progress.Tracker) (err error) {
	ctx, root := tracker.Begin(ctx, progress.Step{Kind: kindAlbumPublish, Name: "winter"})
	defer func() { root.End(err) }()

	var files []photo
	for i := range 12 {
		files = append(files, photo{
			name: fmt.Sprintf("snow-%02d.jpg", i+1),
			size: int64(1_000_000 + (i*700_000)%3_000_000),
			took: time.Duration(1000+(i*450)%2000) * time.Millisecond,
		})
	}
	batchCtx, batch := progress.Batch(ctx, progress.Step{Kind: kindCDNUpload}, len(files))
	defer func() { batch.End(err) }()

	group, groupCtx := errgroup.WithContext(batchCtx)
	group.SetLimit(4)
	for _, file := range files {
		group.Go(func() error {
			return p.uploadFile(groupCtx, file, false)
		})
	}
	return group.Wait()
}

// unknown reads a source that names its size only halfway, logging as it goes.
func (p publisher) unknown(ctx context.Context, tracker *progress.Tracker) (err error) {
	ctx, root := tracker.Begin(ctx, progress.Step{Kind: kindAlbumImport, Name: "archive"})
	defer func() { root.End(err) }()

	ctx, node := progress.Run(ctx, progress.Step{Kind: kindFile, ID: "archive.zip"}, progress.MeasureBytes)
	defer func() { node.End(err) }()

	var (
		logger  = zerolog.Ctx(ctx)
		sampler = progress.NewSampler(node, progress.Sample{Bytes: &progress.Bytes{}})
		size    = int64(24_000_000)
		step    = size / 40
	)
	for done := step; done <= size; done += step {
		if err = p.clock.Sleep(ctx, 100*time.Millisecond); err != nil {
			return err
		}
		switch done {
		case 8 * step:
			logger.Info().Msg("the archive streams its index last")
		case 12 * step:
			logger.Warn().Str("file", "archive.zip").Msg("no size header, counting as it goes")
		case 16 * step:
			logger.Error().Int("chunk", 16).Msg("checksum mismatch, chunk read again")
		}
		sampler.Update(func(sample *progress.Sample) {
			sample.Bytes.Done = done
			if done >= size/2 {
				sample.Bytes.Total = size
			}
		})
	}
	node.Done(progress.Sample{Bytes: &progress.Bytes{Done: size, Total: size}})
	return nil
}

// pauseQueue is a summary activity: one call handles every worker, each a resource.
func (p publisher) pauseQueue(ctx context.Context) (err error) {
	_, node := progress.Run(ctx, progress.Step{Kind: kindQueuePause}, progress.MeasureResources)
	defer func() { node.End(err) }()

	for i := range 4 {
		resource := progress.Resource{Kind: resourceWorker, ID: fmt.Sprintf("worker-%d", i+1)}
		node.UpdateResource(resource, "Draining")
		if err = p.clock.Sleep(ctx, 300*time.Millisecond); err != nil {
			return err
		}
		node.UpdateResource(resource, "Paused")
	}
	return nil
}

func (p publisher) resumeQueue(ctx context.Context) (err error) {
	_, node := progress.Run(ctx, progress.Step{Kind: kindQueueResume}, progress.MeasureResources)
	defer func() { node.End(err) }()

	for i := range 4 {
		if err = p.clock.Sleep(ctx, 100*time.Millisecond); err != nil {
			return err
		}
		node.UpdateResource(progress.Resource{Kind: resourceWorker, ID: fmt.Sprintf("worker-%d", i+1)}, "Running")
	}
	return nil
}

// exportAlbum is a shared workflow, lazily opened: it does not know which root it runs under.
func (p publisher) exportAlbum(ctx context.Context, photos []photo) (err error) {
	ctx, node := progress.Begin(ctx, progress.Step{Kind: kindAlbumExport})
	defer func() { node.End(err) }()

	if err = p.clock.Sleep(ctx, 400*time.Millisecond); err != nil {
		return err
	}
	node.Warn("2 raw files skipped")
	batchCtx, batch := progress.Batch(ctx, progress.Step{Kind: kindPhotoResize}, len(photos))
	for _, item := range photos {
		if err = p.resizePhoto(batchCtx, item); err != nil {
			batch.End(err)
			return err
		}
	}
	batch.End(nil)

	_, manifest := progress.Run(ctx, progress.Step{Kind: kindManifestWrite})
	err = p.clock.Sleep(ctx, 300*time.Millisecond)
	manifest.End(err)
	return err
}

func (p publisher) resizePhoto(ctx context.Context, item photo) (err error) {
	ctx, node := progress.Run(ctx, progress.Step{Kind: kindPhoto, ID: item.name}, progress.MeasureBytes)
	defer func() { node.End(err) }()

	if err = p.read(ctx, item, item.size); err != nil {
		return err
	}
	node.Done(progress.Sample{Bytes: &progress.Bytes{Done: item.size, Total: item.size, Stored: item.size / 5}})
	return nil
}

// uploadFiles is a batch whose first failure cancels the rest.
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
	ctx, node := progress.Run(ctx, progress.Step{Kind: kindFile, ID: item.name}, progress.MeasureBytes)
	defer func() { node.End(err) }()

	if err = ctx.Err(); err != nil {
		return err
	}
	if deny {
		half := photo{
			name: item.name,
			size: item.size / 2,
			took: item.took / 2,
		}
		if err = p.read(ctx, half, item.size); err != nil {
			return err
		}
		// The reader's throttled count lags; the failure reports what was read.
		node.Progress(progress.Sample{Bytes: &progress.Bytes{Done: half.size, Total: item.size}})
		node.Fail("unable to upload", errDenied)
		return errDenied
	}
	if err = p.read(ctx, item, item.size); err != nil {
		return err
	}
	node.Done(progress.Sample{Bytes: &progress.Bytes{Done: item.size, Total: item.size}})
	return nil
}

// rollbackAlbum purges what was uploaded; a purge that fails only warns.
func (p publisher) rollbackAlbum(ctx context.Context, uploaded []photo) (err error) {
	ctx, node := progress.Begin(ctx, progress.Step{Kind: kindAlbumRollback})
	defer func() { node.End(err) }()

	ctx, batch := progress.Batch(ctx, progress.Step{Kind: kindCDNPurge}, len(uploaded))
	defer func() { batch.End(err) }()
	for i, item := range uploaded {
		_, purge := progress.Run(ctx, progress.Step{Kind: kindFile, ID: item.name})
		// The purge runs past the cancellation that triggered it.
		if err = p.clock.Sleep(context.WithoutCancel(ctx), 250*time.Millisecond); err != nil {
			purge.End(err)
			return err
		}
		if i == 0 {
			purge.Warn("purge failed, the file stays on the CDN: edge timeout")
		}
		purge.Done(progress.Sample{})
	}
	return nil
}

// read reads item from a slow source counted on the node of ctx against total, as a deep source
// would.
func (p publisher) read(ctx context.Context, item photo, total int64) error {
	var (
		src = progress.NewReader(ctx, &slowReader{
			ctx:       ctx,
			clock:     p.clock,
			remaining: item.size,
			delay:     time.Duration(int64(item.took) * readBuffer / max(item.size, 1)),
		}, total)
		buf = make([]byte, readBuffer)
	)
	for {
		if _, err := src.Read(buf); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
	}
}

// slowReader hands out remaining zero bytes, waiting delay per read.
type slowReader struct {
	ctx       context.Context
	clock     clock
	remaining int64
	delay     time.Duration
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if err := r.clock.Sleep(r.ctx, r.delay); err != nil {
		return 0, err
	}
	n := min(int64(len(p)), r.remaining)
	r.remaining -= n
	return int(n), nil
}
