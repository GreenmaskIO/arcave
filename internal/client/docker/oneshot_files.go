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
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/moby/moby/client"

	"github.com/greenmaskio/arcave/internal/model"
)

// tarModeSticky is the sticky bit as tar spells it (c_ISVTX).
const tarModeSticky = 0o1000

// copyOneShotFiles writes files into the created container containerID, as one
// archive extracted at its root, every entry owned by root.
func copyOneShotFiles(ctx context.Context, api dockerAPI, containerID string, files []model.OneShotFile) error {
	archive, err := newOneShotArchive(files)
	if err != nil {
		return err
	}
	if _, err := api.CopyToContainer(ctx, containerID, client.CopyToContainerOptions{DestinationPath: "/", Content: archive}); err != nil {
		return fmt.Errorf("copy files into container %s: %w", containerID, err)
	}
	return nil
}

// newOneShotArchive is files as a tar rooted at "/", in their order.
func newOneShotArchive(files []model.OneShotFile) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, file := range files {
		if err := writeOneShotFile(tw, file); err != nil {
			return nil, fmt.Errorf("one-shot file %q: %w", file.Path, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close one-shot archive: %w", err)
	}
	return &buf, nil
}

// writeOneShotFile writes one entry: a directory, or a file and its content.
func writeOneShotFile(tw *tar.Writer, file model.OneShotFile) error {
	switch {
	case !path.IsAbs(file.Path) || path.Clean(file.Path) != file.Path || file.Path == "/":
		return errors.New("not an absolute, clean path below /")
	case file.Mode.Type()&^fs.ModeDir != 0 || file.Mode&(fs.ModeSetuid|fs.ModeSetgid) != 0:
		return fmt.Errorf("mode %v: only a directory or a regular file, sticky at most", file.Mode)
	case file.Mode.IsDir() && len(file.Content) > 0:
		return errors.New("a directory with content")
	}
	if err := tw.WriteHeader(newOneShotHeader(file)); err != nil {
		return err
	}
	_, err := tw.Write(file.Content)
	return err
}

// newOneShotHeader is file's tar header, rooted at "/" and owned by root.
func newOneShotHeader(file model.OneShotFile) *tar.Header {
	var (
		name     = strings.TrimPrefix(file.Path, "/")
		typeflag = byte(tar.TypeReg)
		mode     = int64(file.Mode.Perm())
	)
	if file.Mode.IsDir() {
		name += "/"
		typeflag = tar.TypeDir
	}
	if file.Mode&fs.ModeSticky != 0 {
		mode |= tarModeSticky
	}
	return &tar.Header{
		Name:     name,
		Typeflag: typeflag,
		Mode:     mode,
		Size:     int64(len(file.Content)),
	}
}
