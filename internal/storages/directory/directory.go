// Copyright 2023 Greenmask
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

// Package directory is the local-filesystem Storager backend, vendored from
// greenmask (internal/storages/directory) and adapted for gm-tools: its
// self-referential SubStorage/ListDir returns and Stat now name model.Storager /
// *model.ObjectStat (see internal/model/storager.go), and it gains a no-op Close
// so the interface's per-command lifecycle hook is satisfied.
//
// The code is unix-oriented (it inspects syscall.Errno for ENOENT); a future
// Windows port would need to revisit that.
package directory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/greenmaskio/gm-tools/internal/model"
)

// dirMode/fileMode set the permissions for created directories and files.
// Upstream greenmask uses 0750/0650; gm-tools widens them to the conventional
// 0755/0644 to preserve the behaviour of the os.MkdirAll(0o755)/os.Create the
// snapshot backend used before adopting the Storager.
const (
	dirMode  os.FileMode = 0o755
	fileMode os.FileMode = 0o644
)

type Storage struct {
	dirMode  os.FileMode
	fileMode os.FileMode
	cwd      string
	mx       sync.Mutex
}

// compile-time assertion that the vendored backend satisfies the gm-tools
// interface (which repoints the self-referential returns).
var _ model.Storager = (*Storage)(nil)

func NewStorage(cfg *Config) (*Storage, error) {
	// TODO: We would replace hardcoded file mask to Umask for unix system
	fileInfo, err := os.Stat(cfg.Path)
	if err != nil {
		return nil, err
	}
	if !fileInfo.IsDir() {
		return nil, errors.New("received directory path is file")
	}
	return &Storage{
		dirMode:  dirMode,
		fileMode: fileMode,
		cwd:      cfg.Path,
	}, nil
}

func (s *Storage) GetCwd() string {
	return s.cwd
}

func (s *Storage) Dirname() string {
	return filepath.Base(s.cwd)
}

func (s *Storage) ListDir(ctx context.Context) (files []string, dirs []model.Storager, err error) {
	entries, err := os.ReadDir(s.cwd)
	if err != nil {
		return nil, nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(
				dirs, &Storage{
					cwd:      path.Join(s.cwd, entry.Name()),
					dirMode:  s.dirMode,
					fileMode: s.fileMode,
				},
			)
		} else {
			files = append(files, entry.Name())
		}
	}
	return
}

func (s *Storage) GetObject(ctx context.Context, filePath string) (reader io.ReadCloser, err error) {
	reader, err = os.Open(path.Join(s.cwd, filePath))
	return
}

func (s *Storage) PutObject(ctx context.Context, filePath string, body io.Reader) error {
	_, err := os.Stat(path.Join(s.cwd, path.Dir(filePath)))
	var errNo syscall.Errno
	if err != nil && errors.As(err, &errNo) && errNo == 0x2 {
		s.mx.Lock()
		if err = os.MkdirAll(path.Join(s.cwd, path.Dir(filePath)), s.dirMode); err != nil {
			s.mx.Unlock()
			return fmt.Errorf("error creating directory: %w", err)
		}
		s.mx.Unlock()
	} else if err != nil {
		return fmt.Errorf("error getting file stat: %w", err)
	}
	f, err := os.Create(path.Join(s.cwd, filePath))
	if err != nil {
		return fmt.Errorf("unable to create file: %w", err)
	}

	done := make(chan struct{})
	go func() {
		_, err = io.Copy(f, body)
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}

	if err != nil {
		return fmt.Errorf("error writing data: %w", err)
	}
	return nil
}

func (s *Storage) Delete(ctx context.Context, filePaths ...string) error {
	for _, fp := range filePaths {
		fileInfo, err := os.Stat(path.Join(s.cwd, fp))
		if err != nil {
			return err
		}
		if fileInfo.IsDir() {
			err = os.RemoveAll(path.Join(s.cwd, fp))
			if err != nil {
				return fmt.Errorf(`error deliting directory %s: %w`, fp, err)
			}
		} else {
			err = os.Remove(path.Join(s.cwd, fp))
			if err != nil {
				return fmt.Errorf(`error deliting file %s: %w`, fp, err)
			}
		}
	}
	return nil
}

func (s *Storage) DeleteAll(ctx context.Context, pathPrefix string) error {
	fileInfo, err := os.Stat(path.Join(s.cwd, pathPrefix))
	if err != nil {
		return err
	}
	if fileInfo.IsDir() {
		err = os.RemoveAll(path.Join(s.cwd, pathPrefix))
		if err != nil {
			return fmt.Errorf(`error deliting directory %s: %w`, pathPrefix, err)
		}
	} else {
		err = os.Remove(path.Join(s.cwd, pathPrefix))
		if err != nil {
			return fmt.Errorf(`error deliting file %s: %w`, pathPrefix, err)
		}
	}
	return nil
}

func (s *Storage) Exists(ctx context.Context, fileName string) (bool, error) {
	_, err := os.Stat(path.Join(s.cwd, fileName))
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Storage) SubStorage(dp string, relative bool) model.Storager {
	dirPath := dp
	if relative {
		dirPath = path.Join(s.cwd, dp)
	}
	return &Storage{
		cwd:      dirPath,
		dirMode:  s.dirMode,
		fileMode: s.fileMode,
	}
}

func (s *Storage) Stat(fileName string) (*model.ObjectStat, error) {
	fullPath := path.Join(s.cwd, fileName)
	fileInfo, err := os.Stat(fullPath)
	var errNo syscall.Errno
	if err != nil && errors.As(err, &errNo) && errNo == 0x2 {
		return &model.ObjectStat{
			Name:         fullPath,
			LastModified: time.Time{},
			Exist:        false,
		}, nil
	} else if err != nil {
		return nil, fmt.Errorf("error getting file stat: %w", err)
	}

	return &model.ObjectStat{
		Name:         fullPath,
		LastModified: fileInfo.ModTime(),
		Exist:        true,
	}, nil
}

// Close releases backend resources. The directory backend holds none, so this
// is a no-op; it exists so main.go can own a uniform per-command lifecycle
// across storage backends.
func (s *Storage) Close() error {
	return nil
}
