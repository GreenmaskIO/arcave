package resticrest

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog"
)

// The handlers below are the protocol and nothing else: each reads what the
// request means, calls the repository once, and renders the answer. Every one
// of them is registered in New's table, and they appear here in that order.

// createRepo answers `restic init`'s POST /?create=true.
//
// The query parameter is required: a bare POST / is not an init, and answering
// it 200 would report a repository that was never asked for. What creating one
// costs is the repository's business — here it is nothing.
func (h *handler) createRepo(w http.ResponseWriter, r *http.Request) {
	if create := r.URL.Query().Get("create"); create != "true" {
		// WARN: the 400 on the wire says only "bad request", and the one thing
		// worth knowing about it is what the query actually carried — a POST /
		// with no create at all is a client that is not restic, and
		// create=false or create=1 is a repository URL that was rewritten on
		// the way here.
		zerolog.Ctx(r.Context()).Warn().Str("create", create).Str("query", r.URL.RawQuery).
			Msg("POST / without create=true is not a repository init")
		httpError(w, http.StatusBadRequest)
		return
	}
	if err := h.repo.Create(r.Context()); err != nil {
		h.fail(w, r, err)
	}
}

// statConfig answers HEAD /config: restic asks it to decide whether the
// repository is initialised, so the size matters as much as the status.
func (h *handler) statConfig(w http.ResponseWriter, r *http.Request) {
	size, err := h.repo.StatConfig(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeContentLength(w, size)
}

// getConfig streams the repository config.
//
// It does not go through ServeContent, unlike a blob: the config is small, read
// whole, and never fetched by range, and rest-server answers it with a plain
// write too. Keeping it plain is what makes a Range header on /config behave
// identically on both servers.
func (h *handler) getConfig(w http.ResponseWriter, r *http.Request) {
	src, err := h.repo.OpenConfig(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer func() {
		if err := src.Close(); err != nil {
			zerolog.Ctx(r.Context()).Debug().Err(err).Msg("close config object")
		}
	}()

	// A copy that dies mid-body cannot be turned into a status any more — the
	// 200 is already on the wire — so it is logged and the connection carries a
	// short body, which is what any streaming server does. The byte count is
	// what makes the line diagnostic: it is the length of the truncated body
	// restic then failed to parse.
	if n, err := io.Copy(w, src); err != nil {
		zerolog.Ctx(r.Context()).Error().Err(err).Int64("bytes", n).Msg("stream config object")
	}
}

// saveConfig answers init's write. The 403 an existing config earns is
// errConfigExists, mapped in fail.
func (h *handler) saveConfig(w http.ResponseWriter, r *http.Request) {
	if err := h.repo.SaveConfig(r.Context(), r.Body); err != nil {
		h.fail(w, r, err)
	}
}

// deleteConfig removes the config. It is idempotent — the repository swallows
// the absence — so an object that is already gone is a 200, not a 404.
func (h *handler) deleteConfig(w http.ResponseWriter, r *http.Request) {
	if err := h.repo.DeleteConfig(r.Context()); err != nil {
		h.fail(w, r, err)
	}
}

// listBlobs answers GET /{type}/, in whichever of the two formats Accept asks
// for — exactly as in rest-server: the v2 media type asks for [{name,size}],
// anything else, including no Accept at all, gets the v1 [name] array. Both are
// answered with the matching Content-Type, which is how the client knows which
// it got.
//
// The negotiation is the whole of what this adds to the repository's answer:
// which of the two shapes the same objects are rendered in is an HTTP question,
// and the only one on this path.
func (h *handler) listBlobs(w http.ResponseWriter, r *http.Request) {
	blobs, err := h.repo.ListBlobs(r.Context(), r.PathValue("type"))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	format := mimeTypeAPIV1
	if r.Header.Get("Accept") == mimeTypeAPIV2 {
		format = mimeTypeAPIV2
	}

	// The count and the format it goes out in, which between them answer the one
	// question a listing ever raises: restic is not seeing objects the store
	// holds. Either the listing was short (the repository skipped keys that are
	// not blobs of this repository) or it was negotiated down to v1 and the
	// sizes went with it — and nothing on the wire tells those apart afterwards.
	zerolog.Ctx(r.Context()).Debug().Int("blobs", len(blobs)).Str("format", format).Msg("listed blobs")

	if format == mimeTypeAPIV2 {
		writeJSON(w, r, mimeTypeAPIV2, blobs)
		return
	}

	names := make([]string, 0, len(blobs))
	for _, b := range blobs {
		names = append(names, b.Name)
	}
	writeJSON(w, r, mimeTypeAPIV1, names)
}

// statBlob answers HEAD /{type}/{name}: restic asks it to decide whether it
// already holds a pack.
func (h *handler) statBlob(w http.ResponseWriter, r *http.Request) {
	size, err := h.repo.StatBlob(r.Context(), r.PathValue("type"), r.PathValue("name"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeContentLength(w, size)
}

// getBlob streams one object, honouring Range: this is how restic reads a
// single blob out of a pack file, and it is the read path that matters for
// performance.
//
// http.ServeContent does the whole Range protocol — parsing, 206,
// Content-Range, 416, If-Range — against the seeker the repository hands back,
// so the semantics are stdlib's, which is exactly what rest-server relies on
// too. The modtime is time.Unix(0, 0) as there: ServeContent reads that as
// "unknown" and emits no Last-Modified, and an empty name keeps it from
// guessing a Content-Type from an extension.
func (h *handler) getBlob(w http.ResponseWriter, r *http.Request) {
	f, err := h.repo.OpenBlob(r.Context(), r.PathValue("type"), r.PathValue("name"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			zerolog.Ctx(r.Context()).Debug().Err(err).Msg("close storage object")
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

// saveBlob streams an upload into the repository. The body is handed over
// unread: nothing on this path needs to see the bytes.
func (h *handler) saveBlob(w http.ResponseWriter, r *http.Request) {
	if err := h.repo.SaveBlob(r.Context(), r.PathValue("type"), r.PathValue("name"), r.Body); err != nil {
		h.fail(w, r, err)
	}
}

// deleteBlob removes one object, idempotently — see deleteConfig.
func (h *handler) deleteBlob(w http.ResponseWriter, r *http.Request) {
	if err := h.repo.DeleteBlob(r.Context(), r.PathValue("type"), r.PathValue("name")); err != nil {
		h.fail(w, r, err)
	}
}

// writeContentLength answers a HEAD with the object's size and no body.
func writeContentLength(w http.ResponseWriter, size int64) {
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
}

// writeJSON marshals the listing before touching the response, so a marshal
// failure can still be reported as a 500 rather than corrupting a body that has
// already begun.
func writeJSON(w http.ResponseWriter, r *http.Request, contentType string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		zerolog.Ctx(r.Context()).Error().Err(err).Str("format", contentType).Msg("marshal listing")
		httpError(w, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	if _, err := w.Write(data); err != nil {
		zerolog.Ctx(r.Context()).Error().Err(err).Int("bytes", len(data)).Msg("write listing")
	}
}
