package resticrest

import (
	"context"
	"fmt"
	"io"

	"github.com/greenmaskio/storages"
)

// storagerFile presents one stored object to http.ServeContent as an
// io.ReadSeekCloser, which is the only thing ServeContent needs and the only
// thing a Storager cannot be.
//
// It is lazy on purpose. Seek is arithmetic over the size Stat reported — it
// opens nothing, which matters because ServeContent's first act is to seek to
// the end and back to measure the object. The stream is opened by the first Read
// after a seek, as a single GetObjectRange from the current position to the end,
// and a later seek throws it away. A plain GET therefore costs one ranged call
// covering the whole object, and a Range request costs one ranged call starting
// at the offset: the bytes before it are never transferred, which is the whole
// reason restic can read one blob out of a large pack.
//
// The context is held in the struct because io.Reader has nowhere to pass one;
// it is the request's, and the file never outlives the request (getBlob closes
// it before returning).
type storagerFile struct {
	ctx   context.Context
	store storages.Storager
	key   string
	size  int64         // from Stat, fetched once in newStoragerFile
	pos   int64         // the next byte to read; may sit past size after a seek
	rc    io.ReadCloser // nil until the first Read; invalidated by a seek
}

// compile-time assertion: ServeContent takes exactly this, and Close is ours.
var _ io.ReadSeekCloser = (*storagerFile)(nil)

// newStoragerFile stats the object and reports a missing one as
// storages.ErrFileNotFound, so the caller answers 404 the same way it does for a
// GetObject failure. The size is read once here: a 206 needs it, and re-statting
// per Read would make a range read O(reads) round trips.
func newStoragerFile(ctx context.Context, store storages.Storager, key string) (*storagerFile, error) {
	st, err := store.Stat(key)
	if err != nil {
		return nil, err
	}
	if !st.Exist {
		return nil, fmt.Errorf("%w: %s", storages.ErrFileNotFound, key)
	}
	return &storagerFile{ctx: ctx, store: store, key: key, size: st.Size}, nil
}

// Read opens the stream on demand and reports EOF at (or past) the end without
// asking the store — a range that cannot yield anything is ErrInvalidRange down
// there, and ServeContent legitimately leaves the position at the end after
// measuring the object.
func (f *storagerFile) Read(p []byte) (int, error) {
	if f.pos >= f.size {
		return 0, io.EOF
	}
	if f.rc == nil {
		rc, err := f.store.GetObjectRange(f.ctx, f.key, f.pos, -1)
		if err != nil {
			return 0, err
		}
		f.rc = rc
	}
	n, err := f.rc.Read(p)
	f.pos += int64(n)
	return n, err
}

// Seek moves the position and, when that actually moves it, drops the open
// stream so the next Read re-opens at the new offset. Seeking to where the
// stream already is costs nothing — which is the common case, since ServeContent
// seeks back to 0 before serving a plain GET.
func (f *storagerFile) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = f.pos + offset
	case io.SeekEnd:
		abs = f.size + offset
	default:
		return 0, fmt.Errorf("storage object %s: invalid whence %d", f.key, whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("storage object %s: negative position %d", f.key, abs)
	}
	if abs != f.pos {
		if err := f.closeStream(); err != nil {
			return 0, err
		}
	}
	f.pos = abs
	return abs, nil
}

// Close releases the open stream, if any. It is safe to call when no Read ever
// happened, which is the case for a 416 or an empty object.
func (f *storagerFile) Close() error {
	return f.closeStream()
}

// closeStream drops the current stream and clears it, so a failed close can
// never leave a stale reader behind for the next Read to use.
func (f *storagerFile) closeStream() error {
	if f.rc == nil {
		return nil
	}
	rc := f.rc
	f.rc = nil
	if err := rc.Close(); err != nil {
		return fmt.Errorf("close storage object %s: %w", f.key, err)
	}
	return nil
}
