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

package podman

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/model"
)

// volumeExportMount is where a volume sits inside its export container; absent from
// the image, so nothing of the image is copied into an empty volume.
const volumeExportMount = "/arcave-data"

// maxExportStderrBytes bounds how much of a failed export's stderr its error carries.
const maxExportStderrBytes = 8 << 10

// volumeExportCmd archives the mount's contents with numeric owners kept, which
// Podman's archive API rewrites to the container's user.
var volumeExportCmd = []string{"tar", "-c", "-f", "-", "-C", volumeExportMount, "."}

// oneShotRunner runs a volume's export container; *docker.Engine satisfies it.
type oneShotRunner interface {
	RunOneShot(ctx context.Context, spec model.OneShotSpec) error
}

// Engine is the docker.Engine for a Podman daemon, whose archive API rewrites every
// owner: a volume is exported by tar instead.
type Engine struct {
	*docker.Engine
	runner oneShotRunner
}

// NewEngine extends engine, connected to a Podman daemon.
func NewEngine(engine *docker.Engine) *Engine {
	return &Engine{
		Engine: engine,
		runner: engine,
	}
}

// OpenVolume streams the volume out as a tar written inside a throwaway container,
// owners and modes kept; closing the stream removes the container.
func (e *Engine) OpenVolume(ctx context.Context, src model.MountSource) (io.ReadCloser, error) {
	if src.Source == "" {
		return nil, fmt.Errorf("cannot reach a %q volume with no source", src.Type)
	}
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	var group errgroup.Group
	group.Go(func() error {
		// A log would hold the archive again, so tar's complaint is read off the attach.
		stderr := &boundedBuffer{limit: maxExportStderrBytes}
		err := e.runner.RunOneShot(ctx, model.OneShotSpec{
			Image:       docker.VolumeToolsImage,
			Cmd:         volumeExportCmd,
			Binds:       []string{src.Source + ":" + volumeExportMount + ":ro"},
			SecurityOpt: docker.VolumeSecurityOpt,
			DiscardLog:  true,
			Stdout:      pw,
			Stderr:      stderr,
		})
		if msg := strings.TrimSpace(stderr.String()); err != nil && msg != "" {
			err = fmt.Errorf("export volume %s: %w (stderr: %s)", src.Source, err, msg)
		} else if err != nil {
			err = fmt.Errorf("export volume %s: %w", src.Source, err)
		}
		//nolint:errcheck // a PipeWriter's Close does not fail; the error travels to the reader
		pw.CloseWithError(err)
		return err
	})
	return &volumeExport{
		PipeReader: pr,
		cancel:     cancel,
		group:      &group,
	}, nil
}

// volumeExport is a running export's stream; a failed export fails its last read.
type volumeExport struct {
	*io.PipeReader
	cancel context.CancelFunc
	group  *errgroup.Group
}

// Close stops the export, if it still runs, and waits for its container to go.
func (v *volumeExport) Close() error {
	//nolint:errcheck // a PipeReader's Close does not fail
	v.PipeReader.Close()
	v.cancel()
	//nolint:errcheck // a reader that stopped early is what failed the export
	v.group.Wait()
	return nil
}

// boundedBuffer keeps the first limit bytes written to it and drops the rest, never failing a write.
type boundedBuffer struct {
	limit int
	buf   bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string { return b.buf.String() }
