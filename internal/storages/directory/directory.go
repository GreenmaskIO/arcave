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
// Writes are not atomic. Storager has no Rename to build atomicity out of, and
// GM does not need it: an interrupted savepoint is discarded and re-created, not
// repaired.
//
// Every method routes its key through resolve, so no key — however it was
// produced, including one read back from a hand-edited record — can name a file
// outside the store, or the store root itself. That gate is what makes a
// DeleteAll of an attacker-chosen key at worst destroy one object inside the
// store.
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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/gm-tools/internal/model"
)

// ErrUnsafeKey is returned for a key that does not name an object strictly
// inside the store. It stays package-local on purpose: no caller matches it
// across a seam, so it is not part of any layer's contract and does not belong
// in model.
var ErrUnsafeKey = errors.New("unsafe storage key")

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

// resolve turns a store-relative key into an absolute path inside the store, or
// rejects it. It is a SAFETY GATE, not a convenience: not every key reaching this
// backend is trusted. A savepoint record (savepoint.json) is a plain file the
// user owns, and its SnapshotRef.Opaque flows straight into GetObject/DeleteAll
// with no path check above this layer — checkBackend validates only the backend
// name, not the path. So a hand-edited "opaque": "../../etc/passwd" (or an id of
// "" / ".") would otherwise read or delete outside the store. resolve is the one
// gate every object-addressing method passes through, and it rejects:
//
//   - "" and "." — they resolve to the store root, so a DeleteAll would wipe the
//     whole store rather than one object in it (the original rm -rf-the-store bug);
//   - an absolute key — a key is relative to the store by contract, and an
//     absolute one only ever reaches here by mistake or by injection;
//   - any key that climbs out of the store ("..", "a/../../b").
//
// The escape check compares the cleaned full path against cwd rather than
// scanning the key for "..", so a key that only *looks* like an escape
// ("a/../b") is still accepted.
func (s *Storage) resolve(key string) (string, error) {
	cleaned := path.Clean(key)
	if key == "" || cleaned == "." || path.IsAbs(cleaned) {
		return "", fmt.Errorf("%w: %q", ErrUnsafeKey, key)
	}
	full := filepath.Join(s.cwd, filepath.FromSlash(cleaned))
	rel, err := filepath.Rel(s.cwd, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q escapes the store", ErrUnsafeKey, key)
	}
	return full, nil
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

func (s *Storage) GetObject(_ context.Context, filePath string) (reader io.ReadCloser, err error) {
	full, err := s.resolve(filePath)
	if err != nil {
		return nil, err
	}
	return os.Open(full)
}

// PutObject writes body to filePath, creating parent dirs lazily. The write is
// not atomic: a cancelled or failed copy leaves a partial object under filePath.
// GM tolerates that — a savepoint that fails mid-write is dropped by namespace
// and re-created, never repaired.
func (s *Storage) PutObject(ctx context.Context, filePath string, body io.Reader) error {
	full, err := s.resolve(filePath)
	if err != nil {
		return err
	}

	parent := filepath.Dir(full)
	_, err = os.Stat(parent)
	var errNo syscall.Errno
	if err != nil && errors.As(err, &errNo) && errNo == 0x2 {
		s.mx.Lock()
		if err = os.MkdirAll(parent, s.dirMode); err != nil {
			s.mx.Unlock()
			return fmt.Errorf("error creating directory: %w", err)
		}
		s.mx.Unlock()
	} else if err != nil {
		return fmt.Errorf("error getting file stat: %w", err)
	}
	f, err := os.Create(full)
	if err != nil {
		return fmt.Errorf("unable to create file: %w", err)
	}
	// Closing f also unblocks the copy goroutine below if it is still running on
	// the ctx.Done() path: its next Write fails and it exits, rather than leaking
	// for the life of the process.
	defer func() {
		if err := f.Close(); err != nil {
			log.Ctx(ctx).Debug().Err(err).Str("path", filePath).Msg("close file")
		}
	}()

	// The copy runs in a goroutine so a blocking Read on body cannot outlive a
	// cancelled context. Its error travels back over a buffered channel rather
	// than through a captured variable, which the select below would race on.
	errCh := make(chan error, 1)
	go func() {
		_, err := io.Copy(f, body)
		errCh <- err
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("error writing data: %w", err)
		}
	}
	return nil
}

func (s *Storage) Delete(_ context.Context, filePaths ...string) error {
	for _, fp := range filePaths {
		if err := s.deleteOne(fp); err != nil {
			return err
		}
	}
	return nil
}

func (s *Storage) DeleteAll(_ context.Context, pathPrefix string) error {
	return s.deleteOne(pathPrefix)
}

// deleteOne removes the object at key, recursively when it is a directory. It
// is the single destructive primitive behind Delete and DeleteAll, which differ
// only in arity — so the resolve gate is applied in exactly one place.
func (s *Storage) deleteOne(key string) error {
	full, err := s.resolve(key)
	if err != nil {
		return err
	}
	fileInfo, err := os.Stat(full)
	if err != nil {
		return err
	}
	if fileInfo.IsDir() {
		if err := os.RemoveAll(full); err != nil {
			return fmt.Errorf("error deleting directory %s: %w", key, err)
		}
		return nil
	}
	if err := os.Remove(full); err != nil {
		return fmt.Errorf("error deleting file %s: %w", key, err)
	}
	return nil
}

func (s *Storage) Exists(_ context.Context, fileName string) (bool, error) {
	full, err := s.resolve(fileName)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(full); err != nil {
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
	fullPath, err := s.resolve(fileName)
	if err != nil {
		return nil, err
	}
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
