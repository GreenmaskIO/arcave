// Package lock serialises the GM workflows that mutate a project.
//
// Two concurrent `gm` runs already fight over `docker compose stop/up`, which is
// destructive regardless of how savepoints are stored: one run's stop races the
// other's up, and a snapshot taken while the services are coming back is not a
// consistent one. A per-project lock turns that race into a fast, clear failure.
//
// Locker is bound to its project root at construction, exactly as a snapshot
// backend is bound to its store, so no path crosses the seam its consumer
// declares (core.projectLocker). Whether the lock is a file, a row, or a lease is
// this package's business alone.
//
// The lock is HOST-LOCAL. flock guards a file on the local filesystem, so it
// serialises processes on this machine only. It does NOT protect a savepoint
// store shared between machines — two hosts pointed at the same NFS or S3 store
// can still collide, and nothing here would notice.
package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/greenmaskio/gm-tools/internal/model"
)

// fileName is the lock file, relative to the project root. It is created empty
// and never removed: unlinking it would let a second process acquire a lock on a
// file the first still holds open, since flock guards the inode, not the name.
var fileName = filepath.Join(".gm", "lock")

// Locker takes the exclusive lock of one project. It is bound to that project at
// construction and holds no state between calls, so a single instance is built
// at the DI root and shared.
type Locker struct {
	projectRoot string
}

// New returns the Locker for the project rooted at projectRoot.
func New(projectRoot string) *Locker {
	return &Locker{projectRoot: projectRoot}
}

// Acquire takes the project's exclusive lock, failing immediately with
// model.ErrProjectLocked (rather than blocking) when another process holds it.
// The returned release closes the file, which drops the lock; the kernel also
// drops it if the process dies, so a crashed run never leaves the project wedged.
//
// ctx is unused by the flock syscall, which cannot block here — LOCK_NB returns
// at once. It is in the contract because the consumer's seam must survive a
// locker that reaches a network (a lease in the shared store), which is the only
// kind of lock that could serialise two *hosts*.
func (l *Locker) Acquire(ctx context.Context) (release func() error, err error) {
	path := filepath.Join(l.projectRoot, fileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Closing f here does not drop a lock we never took.
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, model.ErrProjectLocked
		}
		return nil, fmt.Errorf("lock project %s: %w", path, err)
	}

	// Closing the descriptor releases the flock, so release is just Close. The
	// file itself stays on disk.
	return f.Close, nil
}
