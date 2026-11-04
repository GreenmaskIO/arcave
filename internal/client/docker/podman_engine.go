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

package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
)

// volumeExportCmd archives the mount's contents with numeric owners kept, which
// Podman's archive API rewrites to the container's user.
var volumeExportCmd = []string{"tar", "-c", "-f", "-", "-C", volumeHolderMount, "."}

// PodmanEngine is the Engine for a Podman daemon: an image is committed and saved
// through libpod, a volume exported by tar.
type PodmanEngine struct {
	*Engine
	libpod Libpod
}

// NewPodmanEngine extends engine, connected to a Podman daemon, with libpod over the
// same connection.
func NewPodmanEngine(engine *Engine, libpod Libpod) *PodmanEngine {
	return &PodmanEngine{
		Engine: engine,
		libpod: libpod,
	}
}

// CommitContainer commits a stopped container's writable layer through libpod in the OCI
// format, so its id survives a save and a load; comment is dropped, OCI carries none.
func (e *PodmanEngine) CommitContainer(
	ctx context.Context, containerID, _ string, labels map[string]string,
) (model.ImageDigestID, error) {
	query := url.Values{
		"container": {containerID},
		"format":    {"oci"},
		"pause":     {"false"},
		"changes":   labelChanges(labels),
	}
	resp, err := callLibpod(ctx, e.libpod, http.MethodPost, query, "commit")
	if err != nil {
		return "", fmt.Errorf("commit container %s: %w", containerID, err)
	}
	//nolint:errcheck // the body is read whole below
	defer resp.Body.Close()
	var committed struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&committed); err != nil {
		return "", fmt.Errorf("commit container %s: decode libpod's answer: %w", containerID, err)
	}
	imageID, err := model.ParseImageDigestID(withDigestAlgorithm(committed.ID))
	if err != nil {
		return "", fmt.Errorf("commit container %s: %w", containerID, err)
	}
	return imageID, nil
}

// SaveImage streams one image out as an OCI archive, through libpod. The caller
// owns the stream and closes it.
func (e *PodmanEngine) SaveImage(ctx context.Context, imageID model.ImageDigestID) (io.ReadCloser, error) {
	resp, err := callLibpod(ctx, e.libpod, http.MethodGet, url.Values{"format": {"oci-archive"}},
		"images", imageID.String(), "get")
	if err != nil {
		return nil, fmt.Errorf("save image %s: %w", imageID, err)
	}
	return resp.Body, nil
}

// OpenVolume streams the volume out as a tar written inside a throwaway container,
// owners and modes kept; closing the stream removes the container.
func (e *PodmanEngine) OpenVolume(ctx context.Context, src model.MountSource) (io.ReadCloser, error) {
	if src.Source == "" {
		return nil, fmt.Errorf("cannot reach a %q volume with no source", src.Type)
	}
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	var group errgroup.Group
	group.Go(func() error {
		// A log would hold the archive again, so tar's complaint is read off the attach.
		stderr := &boundedBuffer{limit: maxContainerOutputBytes}
		err := runOneShotContainer(ctx, e.api, model.OneShotSpec{
			Image:       volumeToolsImage,
			Cmd:         volumeExportCmd,
			Binds:       []string{src.Source + ":" + volumeHolderMount + ":ro"},
			SecurityOpt: volumeSecurityOpt,
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

// boundedBuffer keeps the first limit bytes written to it and drops the rest,
// never failing a write.
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
