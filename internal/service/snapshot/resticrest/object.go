package resticrest

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/storages"
)

// createRepo answers `restic init`'s POST /?create=true.
//
// It is a no-op that reports success. rest-server creates the repository
// directory, the five type directories and data/00..ff; a Storager has no
// directories to create — the backend materialises whatever parents a key needs
// when the object is written — so there is nothing to do and nothing that could
// fail. The query parameter is still required: a bare POST / is not an init, and
// answering it 200 would report a repository that was never asked for.
func (h *handler) createRepo(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("create") != "true" {
		httpError(w, http.StatusBadRequest)
		return
	}
	zerolog.Ctx(r.Context()).Debug().Str("store", h.store.GetCwd()).Msg("restic repository created")
}

// statObject answers HEAD for the config and for a blob: the object's size in
// Content-Length, or a 404. restic uses it to ask "is this repository
// initialised?" and "do I already have this blob?", so the size matters as much
// as the status.
func (h *handler) statObject(w http.ResponseWriter, r *http.Request, key string) {
	st, err := h.store.Stat(key)
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	if !st.Exist {
		httpError(w, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size, 10))
}

// getConfig streams the repository config.
//
// It does not go through ServeContent, unlike a blob: the config is small, read
// whole, and never fetched by range, and rest-server answers it with a plain
// write too. Keeping it plain is what makes a Range header on /config behave
// identically on both servers.
func (h *handler) getConfig(w http.ResponseWriter, r *http.Request) {
	src, err := h.store.GetObject(r.Context(), configKey)
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	defer func() {
		if err := src.Close(); err != nil {
			zerolog.Ctx(r.Context()).Debug().Err(err).Msg("close config object")
		}
	}()

	// A copy that dies mid-body cannot be turned into a status any more — the
	// 200 is already on the wire — so it is logged and the connection carries a
	// short body, which is what any streaming server does.
	if _, err := io.Copy(w, src); err != nil {
		zerolog.Ctx(r.Context()).Error().Err(err).Msg("stream config object")
	}
}

// saveConfig writes the config at init time, and refuses to replace one that is
// already there.
//
// The 403 is the whole point: rest-server opens the file O_EXCL so that a restic
// init can never silently re-initialise a repository that already holds data,
// and restic reports exactly that to the user. The Exists/PutObject pair is not
// atomic and does not need to be — init is a single client doing one thing.
func (h *handler) saveConfig(w http.ResponseWriter, r *http.Request) {
	exists, err := h.store.Exists(r.Context(), configKey)
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	if exists {
		httpError(w, http.StatusForbidden)
		return
	}
	if err := h.store.PutObject(r.Context(), configKey, r.Body); err != nil {
		h.storeError(w, r, err)
	}
}

// deleteObject removes the config or a blob, idempotently: an object that is
// already gone is a success, not a 404.
//
// That is rest-server's behaviour and restic depends on it — it retries deletes,
// and a retry that lands after the first attempt succeeded must not fail a
// prune. arcave's Delete is the opposite (a missing object is an error, so a
// caller that meant to delete something learns it did not), so the difference is
// absorbed here rather than in the store.
//
// Deleting straight away and swallowing the absence beats asking Exists first:
// one round trip instead of two, and no window between the question and the act.
//
// (Append-only mode, when it lands, refuses here — for every type but locks.)
func (h *handler) deleteObject(w http.ResponseWriter, r *http.Request, key string) {
	if err := h.store.Delete(r.Context(), key); err != nil && !errors.Is(err, storages.ErrFileNotFound) {
		h.storeError(w, r, err)
	}
}

// getBlob streams an object, honouring Range: this is how restic reads a single
// blob out of a pack file, and it is the read path that matters for performance.
//
// http.ServeContent does the whole Range protocol — parsing, 206, Content-Range,
// 416, If-Range — against the lazy seeker in file.go, so the semantics are
// stdlib's, which is exactly what rest-server relies on too. The modtime is
// time.Unix(0, 0) as there: ServeContent reads that as "unknown" and emits no
// Last-Modified, and an empty name keeps it from guessing a Content-Type from an
// extension.
func (h *handler) getBlob(w http.ResponseWriter, r *http.Request, key string) {
	f, err := newStoragerFile(r.Context(), h.store, key)
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			zerolog.Ctx(r.Context()).Debug().Err(err).Str("key", key).Msg("close storage object")
		}
	}()

	// Declaring the type is not cosmetic: without it ServeContent sniffs, and a
	// sniff is a 512-byte read from offset 0 followed by a seek back — a whole
	// extra ranged fetch on every GET, including the ranged ones this exists to
	// keep cheap. Every object restic stores is encrypted, so the sniff would
	// have answered application/octet-stream anyway.
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", time.Unix(0, 0), f)
}

// saveBlob stores an uploaded pack, index, snapshot, key or lock.
//
// The body is streamed straight into the store: no temp object, no rename, no
// fsync, and no check that the content hashes to the name in the URL (which
// rest-server itself makes optional). Blob names are content-addressed, so an
// upload of a name that already exists carries the same bytes — arcave takes the
// overwrite rather than rest-server's 403, because a retried upload after a
// half-written object is the case that matters, and it must not be refused.
//
// (Append-only mode, when it lands, refuses an overwrite here.)
func (h *handler) saveBlob(w http.ResponseWriter, r *http.Request, key string) {
	if err := h.store.PutObject(r.Context(), key, r.Body); err != nil {
		h.storeError(w, r, err)
	}
}
