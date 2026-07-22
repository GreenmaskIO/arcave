package model

import (
	"context"
	"io"
	"time"
)

// ObjectStat is the metadata a Storager reports for one object. It mirrors
// greenmask's storages/domains.ObjectStat so the vendored directory backend can
// satisfy Storager without depending on greenmask's internal packages.
type ObjectStat struct {
	Name         string
	LastModified time.Time
	Exist        bool
}

// Storager is the storage backend contract arcave's snapshot flows speak. It is the
// greenmask storages.Storager surface, re-declared here (in the shared model
// package) so the concrete backend is swappable and every layer already imports
// it. The self-referential SubStorage/ListDir returns and Stat name arcave'
// own types (model.Storager / *model.ObjectStat) rather than greenmask's, which
// is why the interface is owned here and the backend is vendored to match.
//
// arcave's snapshot flows use only GetObject, PutObject, Exists, Delete, DeleteAll,
// SubStorage, ListDir and Close; GetCwd, Dirname and Stat are carried for parity
// with the upstream surface so an alternate backend drops in unchanged.
type Storager interface {
	// GetCwd returns the current working directory (CWD) path.
	GetCwd() string
	// Dirname returns the current dirname without prefix.
	Dirname() string
	// ListDir walks the storage and returns the files and sub-directories in the cwd.
	ListDir(ctx context.Context) (files []string, dirs []Storager, err error)
	// GetObject returns a ReadCloser for the object at filePath.
	GetObject(ctx context.Context, filePath string) (reader io.ReadCloser, err error)
	// PutObject writes body to the object at filePath, creating parent dirs lazily.
	PutObject(ctx context.Context, filePath string, body io.Reader) error
	// Delete removes the objects at the provided paths.
	Delete(ctx context.Context, filePaths ...string) error
	// DeleteAll removes every object under the provided path prefix.
	DeleteAll(ctx context.Context, pathPrefix string) error
	// Exists reports whether an object exists.
	Exists(ctx context.Context, fileName string) (bool, error)
	// SubStorage returns a new Storager sharing this config but re-rooted at
	// subPath; if relative is true, subPath is a sub-folder of the current cwd.
	SubStorage(subPath string, relative bool) Storager
	// Stat returns the metadata for an object.
	Stat(fileName string) (*ObjectStat, error)
	// Close releases any backend resources. It is a per-command lifecycle hook
	// (a no-op for the directory backend, meaningful for future remote ones).
	Close() error
}
